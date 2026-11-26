package main

import (
	"context"
	"errors"
	"net/http"
	"time"

	"briefstudio/web/internal/chatgpt"
	"briefstudio/web/internal/engine"
	"briefstudio/web/internal/schedule"
)

type scheduleSettings struct {
	Enabled      bool   `json:"enabled"`
	Time         string `json:"time"`
	TimeZone     string `json:"timeZone"`
	WeekdaysOnly bool   `json:"weekdaysOnly"`
	Model        string `json:"model"`
	GEXMode      string `json:"gexMode"`
}

func (s *studioServer) scheduleState(w http.ResponseWriter, _ *http.Request) {
	if s.schedule == nil {
		writeError(w, http.StatusServiceUnavailable, "La planification est indisponible dans ce profil.")
		return
	}
	s.mu.Lock()
	session := s.session
	s.mu.Unlock()
	owner := session.ScheduleOwner()
	state := s.schedule.State()
	config := state.Config
	authorized := owner != "" && owner == config.Owner
	response := struct {
		Config       scheduleSettings `json:"config"`
		Last         *schedule.Result `json:"last,omitempty"`
		Next         string           `json:"next,omitempty"`
		CanEnable    bool             `json:"canEnable"`
		NeedsConsent bool             `json:"needsConsent"`
	}{
		Config: scheduleSettings{config.Enabled, config.Time, config.TimeZone, config.WeekdaysOnly, config.Model, config.GEXMode},
		Last:   state.Last, CanEnable: owner != "", NeedsConsent: config.Enabled && !authorized,
	}
	if next := state.Next(time.Now()); authorized && !next.IsZero() {
		response.Next = next.Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *studioServer) changeSchedule(w http.ResponseWriter, r *http.Request) {
	if s.schedule == nil {
		writeError(w, http.StatusServiceUnavailable, "La planification est indisponible dans ce profil.")
		return
	}
	var req struct {
		scheduleSettings
		Consent bool `json:"consent"`
	}
	if readJSON(w, r, &req) != nil {
		writeError(w, http.StatusBadRequest, "Réglages de planification invalides.")
		return
	}
	if !req.Enabled {
		s.mu.Lock()
		err := s.schedule.Update(schedule.Config{}, time.Now())
		s.mu.Unlock()
		if err != nil {
			writeScheduleError(w, err)
			return
		}
		s.scheduleState(w, r)
		return
	}
	if !req.Consent {
		writeError(w, http.StatusBadRequest, "Autorise l’utilisation automatique de ton forfait ChatGPT pour activer la planification.")
		return
	}
	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		writeError(w, http.StatusConflict, "Une opération est en cours. Réessaie une fois terminée.")
		return
	}
	owner := s.session.ScheduleOwner()
	if owner == "" {
		s.mu.Unlock()
		writeError(w, http.StatusUnauthorized, "Connecte ton compte ChatGPT pour activer la planification.")
		return
	}
	if !modelAvailable(s.models, req.Model) {
		s.mu.Unlock()
		writeError(w, http.StatusBadRequest, "Choisis un modèle disponible pour ton compte.")
		return
	}
	err := s.schedule.Update(schedule.Config{
		Enabled: true, Time: req.Time, TimeZone: req.TimeZone, WeekdaysOnly: req.WeekdaysOnly,
		Model: req.Model, GEXMode: req.GEXMode, Owner: owner,
	}, time.Now())
	s.mu.Unlock()
	if err != nil {
		writeScheduleError(w, err)
		return
	}
	s.scheduleState(w, r)
}

func writeScheduleError(w http.ResponseWriter, err error) {
	if errors.Is(err, schedule.ErrInvalidConfig) {
		writeError(w, http.StatusBadRequest, "Vérifie l’heure, le fuseau horaire et le mode GEX.")
		return
	}
	writeError(w, http.StatusInternalServerError, "Impossible d’enregistrer la planification sur cet ordinateur.")
}

func (s *studioServer) runScheduledBrief(ctx context.Context, config schedule.Config) schedule.Result {
	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		return schedule.Result{Status: "blocked", Message: "Une autre opération était déjà en cours."}
	}
	session := s.session
	if !config.Enabled || config.Owner == "" || config.Owner != session.ScheduleOwner() {
		s.mu.Unlock()
		return schedule.Result{Status: "blocked", Message: "La connexion ChatGPT ou l’autorisation de planification doit être rétablie."}
	}
	s.busy = true
	s.mu.Unlock()
	defer s.releaseOperation()

	modelsCtx, cancelModels := context.WithTimeout(ctx, 45*time.Second)
	models, err := s.client.Models(modelsCtx, session)
	cancelModels()
	if err != nil {
		return schedule.Result{Status: "failed", Message: err.Error()}
	}
	if !modelAvailable(models, config.Model) {
		return schedule.Result{Status: "blocked", Message: chatgpt.ErrModel.Error()}
	}
	s.mu.Lock()
	s.models = models
	s.mu.Unlock()
	briefCtx, cancelBrief := context.WithTimeout(ctx, 145*time.Second)
	defer cancelBrief()
	id, _, err := s.buildAndSaveBrief(briefCtx, session, briefRequest{Model: config.Model, GEXMode: engine.GEXMode(config.GEXMode)})
	if err != nil {
		return schedule.Result{Status: "failed", Message: err.Error()}
	}
	return schedule.Result{Status: "success", BriefID: id, Message: "Brief NQ/ES enregistré."}
}
