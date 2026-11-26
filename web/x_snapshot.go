package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"briefstudio/web/internal/engine"
	"briefstudio/web/internal/sources"
)

type xSnapshotStatus struct {
	Count      int    `json:"count"`
	Latest     string `json:"latest,omitempty"`
	ObservedAt string `json:"observedAt,omitempty"`
	Stale      bool   `json:"stale"`
}

func (store *sourceStore) xSnapshotPath() string { return filepath.Join(store.dir, "x-snapshot.json") }

// The file can be refreshed externally by a local export. Check it on every use.
func (store *sourceStore) readXSnapshot(now time.Time) (*sources.XSnapshot, bool, error) {
	body, err := readLimited(store.xSnapshotPath(), 16<<10)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, errors.New("Le fichier X local est illisible.")
	}
	var snapshot sources.XSnapshot
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&snapshot) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, true, errors.New("Le fichier X local est invalide.")
	}
	if err := snapshot.Validate(now); err != nil {
		return nil, true, err
	}
	return &snapshot, true, nil
}

func (store *sourceStore) snapshotStatus(now time.Time) *xSnapshotStatus {
	snapshot, present, err := store.readXSnapshot(now)
	if !present {
		return nil
	}
	if err != nil {
		return &xSnapshotStatus{Stale: true}
	}
	items := snapshot.Headlines(now)
	status := &xSnapshotStatus{Count: len(items), ObservedAt: snapshot.ObservedAt.UTC().Format(time.RFC3339), Stale: snapshot.Stale(now)}
	if len(items) != 0 {
		status.Latest = items[0].PublishedAt.Format(time.RFC3339)
	}
	return status
}

func (store *sourceStore) localX(now time.Time) (engine.NewsFeed, bool) {
	snapshot, present, err := store.readXSnapshot(now)
	feed := engine.NewsFeed{Provider: sources.X}
	if present {
		switch {
		case err != nil:
			feed.Failure = err.Error()
		case snapshot.Stale(now):
			feed.Failure = "Le fichier X local date de plus de 36 heures. Importe un relevé récent."
		default:
			feed.Items = snapshot.Headlines(now)
		}
	}
	return feed, present
}

func (s *studioServer) changeXSnapshot(w http.ResponseWriter, r *http.Request) {
	var body struct {
		sources.XSnapshot
		Remove bool `json:"remove"`
	}
	if readJSON(w, r, &body) != nil {
		writeError(w, http.StatusBadRequest, "Le fichier X doit être un instantané JSON de 16 Kio au maximum.")
		return
	}
	path := s.sourceSettings.xSnapshotPath()
	if body.Remove {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			writeError(w, http.StatusInternalServerError, "Impossible de retirer le fichier X.")
			return
		}
	} else {
		if err := body.XSnapshot.Validate(time.Now()); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := writeProfileJSON(path, body.XSnapshot); err != nil {
			writeError(w, http.StatusInternalServerError, "Impossible d’enregistrer le fichier X.")
			return
		}
	}
	s.sourceStatus(w, r)
}

func (s *studioServer) birdSnapshot(w http.ResponseWriter, r *http.Request) {
	if readJSON(w, r, &struct{}{}) != nil {
		writeError(w, http.StatusBadRequest, "Demande Bird invalide.")
		return
	}
	if s.refreshBird == nil {
		writeError(w, http.StatusConflict, "Bird est indisponible sur cet ordinateur.")
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
	snapshot, err := s.refreshBird(r.Context())
	if err != nil {
		writeError(w, sources.Status(err), err.Error())
		return
	}
	if err := snapshot.Validate(time.Now()); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if err := writeProfileJSON(s.sourceSettings.xSnapshotPath(), snapshot); err != nil {
		writeError(w, http.StatusInternalServerError, "Impossible d’enregistrer les publications de Walter.")
		return
	}
	s.sourceStatus(w, r)
}
