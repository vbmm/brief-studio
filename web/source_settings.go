package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"briefstudio/web/internal/engine"
	"briefstudio/web/internal/sources"
)

var optionalProviders = [...]sources.Provider{sources.Discord, sources.X}

type sourceProfile struct {
	settings   sources.Settings
	persistent bool
}

type storedSource struct {
	Target string `json:"target"`
	Token  string `json:"token,omitempty"`
}

type sourceStore struct {
	mu      sync.RWMutex
	dir     string
	entries map[sources.Provider]sourceProfile
}

func openSourceStore(dir string) *sourceStore {
	store := &sourceStore{dir: dir, entries: make(map[sources.Provider]sourceProfile)}
	for _, provider := range optionalProviders {
		body, err := readLimited(store.path(provider), 16<<10)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		var saved storedSource
		if err != nil || json.Unmarshal(body, &saved) != nil || len(saved.Target) > 256 || len(saved.Token) > 4096 {
			log.Printf("Les paramètres locaux de %s n’ont pas pu être lus.", provider.Label())
			continue
		}
		settings := sources.Settings{Target: saved.Target, Token: saved.Token}
		if saved.Token != "" {
			settings, err = sources.Normalize(provider, settings)
			if err != nil {
				log.Printf("Les paramètres locaux de %s ne sont plus valides.", provider.Label())
				continue
			}
		}
		store.entries[provider] = sourceProfile{settings: settings, persistent: saved.Token != ""}
	}
	return store
}

func (store *sourceStore) path(provider sources.Provider) string {
	return filepath.Join(store.dir, "source-"+string(provider)+".json")
}

type sourceStatus struct {
	Target        string           `json:"target"`
	Configured    bool             `json:"configured"`
	Persistent    bool             `json:"persistent"`
	Snapshot      *xSnapshotStatus `json:"snapshot,omitempty"`
	BirdAvailable bool             `json:"birdAvailable,omitempty"`
}

func (store *sourceStore) status() map[sources.Provider]sourceStatus {
	store.mu.RLock()
	defer store.mu.RUnlock()
	result := make(map[sources.Provider]sourceStatus, len(optionalProviders))
	for _, provider := range optionalProviders {
		profile := store.entries[provider]
		result[provider] = sourceStatus{Target: profile.settings.Target, Configured: profile.settings.Token != "", Persistent: profile.persistent}
	}
	x := result[sources.X]
	x.Snapshot = store.snapshotStatus(time.Now())
	result[sources.X] = x
	return result
}

func (store *sourceStore) settings(provider sources.Provider) sources.Settings {
	store.mu.RLock()
	defer store.mu.RUnlock()
	return store.entries[provider].settings
}

type sourceChange struct {
	Target     string `json:"target"`
	Token      string `json:"token"`
	Persistent bool   `json:"persistent"`
	Remove     bool   `json:"remove"`
}

func (store *sourceStore) change(provider sources.Provider, change sourceChange) (int, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	path := store.path(provider)
	if change.Remove {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return http.StatusInternalServerError, errors.New("Impossible d’effacer les paramètres locaux.")
		}
		delete(store.entries, provider)
		return http.StatusOK, nil
	}
	if change.Token == "" {
		change.Token = store.entries[provider].settings.Token
	}
	settings, err := sources.Normalize(provider, sources.Settings{Target: change.Target, Token: change.Token})
	if err != nil {
		return http.StatusBadRequest, err
	}
	saved := storedSource{Target: settings.Target}
	if change.Persistent {
		saved.Token = settings.Token
	}
	if err := writeProfileJSON(path, saved); err != nil {
		return http.StatusInternalServerError, errors.New("Impossible d’enregistrer les paramètres locaux.")
	}
	store.entries[provider] = sourceProfile{settings: settings, persistent: change.Persistent}
	return http.StatusOK, nil
}

func writeProfileJSON(path string, profile any) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".source-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	writeErr := json.NewEncoder(file).Encode(profile)
	syncErr := file.Sync()
	if err := errors.Join(writeErr, syncErr, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func sourceProvider(r *http.Request) sources.Provider {
	return sources.Provider(r.PathValue("provider"))
}

func (s *studioServer) sourceStatus(w http.ResponseWriter, _ *http.Request) {
	states := s.sourceSettings.status()
	x := states[sources.X]
	x.BirdAvailable = s.refreshBird != nil
	states[sources.X] = x
	writeJSON(w, http.StatusOK, states)
}

func (s *studioServer) changeSource(w http.ResponseWriter, r *http.Request) {
	provider := sourceProvider(r)
	if provider != sources.Discord && provider != sources.X {
		writeError(w, http.StatusNotFound, "Source inconnue.")
		return
	}
	var change sourceChange
	if readJSON(w, r, &change) != nil || len(change.Target) > 256 || len(change.Token) > 4096 {
		writeError(w, http.StatusBadRequest, "Paramètres de source invalides.")
		return
	}
	if status, err := s.sourceSettings.change(provider, change); err != nil {
		writeError(w, status, err.Error())
		return
	}
	s.sourceStatus(w, r)
}

func (s *studioServer) testSource(w http.ResponseWriter, r *http.Request) {
	provider := sourceProvider(r)
	if provider != sources.Discord && provider != sources.X {
		writeError(w, http.StatusNotFound, "Source inconnue.")
		return
	}
	if readJSON(w, r, &struct{}{}) != nil {
		writeError(w, http.StatusBadRequest, "Demande de test invalide.")
		return
	}
	if provider == sources.X {
		if feed, present := s.sourceSettings.localX(time.Now()); present {
			if feed.Failure != "" {
				writeError(w, http.StatusConflict, feed.Failure)
			} else {
				writeSourceTestResult(w, feed.Items)
			}
			return
		}
	}
	settings := s.sourceSettings.settings(provider)
	if settings.Token == "" {
		writeError(w, http.StatusConflict, "Enregistre cette source avant de la tester.")
		return
	}
	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		writeError(w, http.StatusConflict, "Une opération est déjà en cours.")
		return
	}
	s.busy = true
	s.mu.Unlock()
	defer s.releaseOperation()
	items, err := s.fetchSource(r.Context(), provider, settings, time.Now())
	if err != nil {
		writeError(w, sources.Status(err), err.Error())
		return
	}
	writeSourceTestResult(w, items)
}

func writeSourceTestResult(w http.ResponseWriter, items []sources.Headline) {
	result := struct {
		Count  int    `json:"count"`
		Latest string `json:"latest,omitempty"`
	}{Count: len(items)}
	var latest time.Time
	for _, item := range items {
		if item.PublishedAt.After(latest) {
			latest = item.PublishedAt
		}
	}
	if !latest.IsZero() {
		result.Latest = latest.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *studioServer) gatherOptionalNews(ctx context.Context, cutoff time.Time) []engine.NewsFeed {
	results := make([]chan engine.NewsFeed, 0, len(optionalProviders))
	for _, provider := range optionalProviders {
		if provider == sources.X {
			if feed, present := s.sourceSettings.localX(cutoff); present {
				ready := make(chan engine.NewsFeed, 1)
				ready <- feed
				results = append(results, ready)
				continue
			}
		}
		settings := s.sourceSettings.settings(provider)
		if settings.Token == "" {
			continue
		}
		result := make(chan engine.NewsFeed, 1)
		results = append(results, result)
		go func() {
			items, err := s.fetchSource(ctx, provider, settings, cutoff)
			feed := engine.NewsFeed{Provider: provider, Items: items}
			if err != nil {
				feed.Failure = err.Error()
			}
			result <- feed
		}()
	}
	var feeds []engine.NewsFeed
	for _, result := range results {
		select {
		case feed := <-result:
			feeds = append(feeds, feed)
		case <-ctx.Done():
			return nil
		}
	}
	return feeds
}
