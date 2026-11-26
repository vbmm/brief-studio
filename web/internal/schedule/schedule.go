// Package schedule keeps a single, user-authorized brief schedule in a local
// profile. The caller holds the application's exclusive profile lock.
package schedule

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

var ErrInvalidConfig = errors.New("Planification invalide")

type Config struct {
	Enabled      bool   `json:"enabled"`
	Time         string `json:"time"`
	TimeZone     string `json:"timeZone"`
	WeekdaysOnly bool   `json:"weekdaysOnly"`
	Model        string `json:"model"`
	GEXMode      string `json:"gexMode"`
	Owner        string `json:"owner,omitempty"`
}

type Result struct {
	At      time.Time `json:"at"`
	Status  string    `json:"status"`
	BriefID string    `json:"briefId,omitempty"`
	Message string    `json:"message,omitempty"`
}

type State struct {
	Config Config  `json:"config"`
	Last   *Result `json:"last"`

	armedAt     time.Time
	claimedDate string
}

type Store struct {
	mu         sync.Mutex
	path       string
	data       diskState
	generation uint64
	cancel     context.CancelFunc
}

func Open(path string, now time.Time) (*Store, error) {
	data, err := load(path)
	if err != nil {
		return nil, err
	}
	if data.Last != nil && data.Last.Status == "running" {
		data.Last = &Result{At: now.UTC(), Status: "interrupted", Message: "L’exécution précédente a été interrompue."}
		if err := save(path, data); err != nil {
			return nil, err
		}
	}
	return &Store{path: path, data: data}, nil
}

func (s *Store) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.state()
}

func (s *Store) Update(config Config, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if config == (Config{}) {
		// Disabling a schedule should not erase the user's form settings.
		config = s.data.Config
		config.Enabled = false
	}
	if now.IsZero() {
		return ErrInvalidConfig
	}
	if err := validateConfig(config); err != nil {
		return err
	}
	if s.data.Config == config {
		return nil
	}

	replacement := s.data
	replacement.Config = config
	replacement.ArmedAt = now.UTC()
	if s.cancel != nil && replacement.Last != nil && replacement.Last.Status == "running" {
		replacement.Last = &Result{At: now.UTC(), Status: "cancelled", Message: "La planification a été modifiée pendant l’exécution."}
	}
	if err := save(s.path, replacement); err != nil {
		return err
	}
	s.data = replacement
	s.generation++
	if s.cancel != nil {
		s.cancel()
	}
	return nil
}

// Tick makes at most one durable claim for a local calendar date. If it
// claims the day, it invokes execute outside the store lock and waits for it.
func (s *Store) Tick(ctx context.Context, now time.Time, execute func(context.Context, Config) Result) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if execute == nil {
		return false, errors.New("Aucune tâche de brief à exécuter.")
	}
	s.mu.Lock()
	config := s.data.Config
	if !config.Enabled || s.cancel != nil {
		s.mu.Unlock()
		return false, nil
	}
	clock, err := loadClock(config)
	if err != nil {
		s.mu.Unlock()
		return false, err
	}
	day := civilDay(now, clock.zone)
	at := s.data.state().availableSlot(clock, day)
	if at.IsZero() || now.Before(at) || now.After(at.Add(wakeWindow)) {
		s.mu.Unlock()
		return false, nil
	}
	claim := s.data
	claim.ClaimedDate = day.Format(dateLayout)
	claim.Last = &Result{At: now.UTC(), Status: "running"}
	if err := save(s.path, claim); err != nil {
		s.mu.Unlock()
		return false, err
	}
	s.data = claim
	generation := s.generation
	runContext, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.mu.Unlock()

	var result Result
	if runContext.Err() == nil {
		result = execute(runContext, config)
	}
	defer cancel()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancel = nil
	if generation != s.generation {
		return true, nil
	}
	if runContext.Err() != nil {
		result = Result{Status: "cancelled", Message: "L’exécution a été annulée."}
	}
	result = terminalResult(result, now)
	completed := s.data
	completed.Last = &result
	if err := save(s.path, completed); err != nil {
		// The claim is already durable. Never retry an uncertain execution.
		s.data.Last = &Result{At: now.UTC(), Status: "interrupted", Message: "Le résultat de l’exécution n’a pas pu être enregistré."}
		return true, err
	}
	s.data = completed
	return true, nil
}

// Run is the local application's in-process timer. Take the time after wake,
// rather than treating old ticker events as current observations.
func (s *Store) Run(ctx context.Context, execute func(context.Context, Config) Result) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if _, err := s.Tick(ctx, time.Now(), execute); err != nil && ctx.Err() == nil {
			log.Printf("Brief Studio : %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func terminalResult(result Result, started time.Time) Result {
	switch result.Status {
	case "success", "failed", "blocked", "cancelled":
	default:
		result = Result{Status: "failed", Message: "La tâche n’a pas renvoyé de résultat valide."}
	}
	if !validText(result.BriefID, 128, false) || !validText(result.Message, 1024, false) {
		result = Result{Status: "failed", Message: "Le résultat de la tâche est invalide."}
	}
	if result.At.IsZero() {
		result.At = started
	}
	result.At = result.At.UTC()
	return result
}

func validateConfig(config Config) error {
	if _, err := loadClock(config); err != nil {
		return err
	}
	switch config.GEXMode {
	case "", "without", "with", "public":
	default:
		return fmt.Errorf("%w: choisis un mode GEX valide.", ErrInvalidConfig)
	}
	if config.Enabled && config.GEXMode == "" {
		return fmt.Errorf("%w: choisis un mode GEX.", ErrInvalidConfig)
	}
	if !validText(config.Model, 160, config.Enabled) || strings.ContainsFunc(config.Model, unicode.IsSpace) {
		return fmt.Errorf("%w: choisis un modèle valide.", ErrInvalidConfig)
	}
	if !validText(config.Owner, 256, config.Enabled) || strings.ContainsFunc(config.Owner, unicode.IsSpace) {
		return fmt.Errorf("%w: autorise l’utilisation du compte ChatGPT actif.", ErrInvalidConfig)
	}
	return nil
}

func validText(value string, length int, required bool) bool {
	return (!required || value != "") && len(value) <= length && utf8.ValidString(value) &&
		!strings.ContainsFunc(value, unicode.IsControl)
}
