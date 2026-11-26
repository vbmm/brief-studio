package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"briefstudio/web/internal/sources"
)

const marketSnapshotMaxBytes = 16 << 10

type marketSnapshotStatus struct {
	Source     string   `json:"source"`
	ObservedAt string   `json:"observedAt"`
	Fresh      bool     `json:"fresh"`
	Gamma      []string `json:"gamma"`
	Levels     []string `json:"levels"`
}

type marketSourceState struct {
	Snapshot *marketSnapshotStatus `json:"snapshot"`
}

func (store *sourceStore) marketSnapshotPath() string {
	return filepath.Join(store.dir, "market-input.json")
}

// Read the current file at each use so a local exporter can replace it atomically.
func (store *sourceStore) readMarketSnapshot(cutoff time.Time) (*sources.MarketSnapshot, error) {
	body, err := readLimited(store.marketSnapshotPath(), marketSnapshotMaxBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("Le fichier de marché local est illisible.")
	}
	var snapshot sources.MarketSnapshot
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&snapshot) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("Le fichier de marché local est invalide.")
	}
	if err := snapshot.Validate(cutoff); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func marketState(snapshot *sources.MarketSnapshot, now time.Time) marketSourceState {
	if snapshot == nil {
		return marketSourceState{}
	}
	status := &marketSnapshotStatus{
		Source: snapshot.Source, ObservedAt: snapshot.ObservedAt.UTC().Format(time.RFC3339),
		Fresh: snapshot.Fresh(now), Gamma: []string{}, Levels: []string{},
	}
	for _, symbol := range []string{"NQ", "ES"} {
		instrument := snapshot.Instruments[symbol]
		if instrument.Gamma != nil {
			status.Gamma = append(status.Gamma, symbol)
		}
		if instrument.Levels != nil {
			status.Levels = append(status.Levels, symbol)
		}
	}
	return marketSourceState{Snapshot: status}
}

func (s *studioServer) marketSourceStatus(w http.ResponseWriter, _ *http.Request) {
	now := time.Now()
	snapshot, err := s.sourceSettings.readMarketSnapshot(now)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, marketState(snapshot, now))
}

func (s *studioServer) readPublicGEX(ctx context.Context, cutoff time.Time) (*sources.MarketSnapshot, error) {
	if s.fetchPublicGEX == nil {
		return nil, sources.ErrPublicGEXUnavailable
	}
	return s.fetchPublicGEX(ctx, cutoff)
}

func writePublicGEXError(w http.ResponseWriter, err error) {
	if errors.Is(err, sources.ErrPublicGEXUnavailable) {
		writeError(w, http.StatusUnprocessableEntity, "Aucun relevé public FlashAlpha exploitable dans les dernières 24 h. Choisis Sans GEX ou importe ton relevé.")
		return
	}
	writeError(w, http.StatusBadGateway, "Le relevé public FlashAlpha est temporairement indisponible.")
}

func (s *studioServer) testPublicGEX(w http.ResponseWriter, r *http.Request) {
	if readJSON(w, r, &struct{}{}) != nil {
		writeError(w, http.StatusBadRequest, "Demande de test GEX invalide.")
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
	cutoff := time.Now()
	snapshot, err := s.readPublicGEX(r.Context(), cutoff)
	if err != nil {
		writePublicGEXError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, marketState(snapshot, cutoff))
}

func (s *studioServer) changeMarketSource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action   string                  `json:"action"`
		Snapshot *sources.MarketSnapshot `json:"snapshot,omitempty"`
	}
	if readJSON(w, r, &req) != nil {
		writeError(w, http.StatusBadRequest, "Choisis un fichier JSON de marché valide de 16 Kio maximum.")
		return
	}
	path := s.sourceSettings.marketSnapshotPath()
	now := time.Now()
	switch {
	case req.Action == "import" && req.Snapshot != nil:
		if err := req.Snapshot.Validate(now); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := writeProfileJSON(path, req.Snapshot); err != nil {
			writeError(w, http.StatusInternalServerError, "Impossible d’enregistrer le fichier de marché.")
			return
		}
	case req.Action == "remove" && req.Snapshot == nil:
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			writeError(w, http.StatusInternalServerError, "Impossible de retirer le fichier de marché.")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "Action de marché invalide.")
		return
	}
	writeJSON(w, http.StatusOK, marketState(req.Snapshot, now))
}

func marketSourceTemplate(w http.ResponseWriter, _ *http.Request) {
	// The historic timestamp keeps these illustrative prices from being used
	// as a current observation if someone imports the example as is.
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="exemple-market-input.json"`)
	_, _ = io.WriteString(w, marketTemplate)
}

const marketTemplate = `{
  "schema": "brief-studio/market-input/v1",
  "observedAt": "2000-01-01T14:00:00Z",
  "source": "Exemple fictif",
  "instruments": {
    "NQ": {
      "symbol": "NQ=F",
      "gamma": {
        "regime": "positive",
        "flip": 20100,
        "callWall": 20300,
        "putWall": 19900
      },
      "levels": {
        "val": 20025,
        "poc": 20125,
        "vah": 20200,
        "vwap": 20100.25
      }
    },
    "ES": {
      "symbol": "ES=F",
      "gamma": {
        "regime": "neutral"
      },
      "levels": {
        "poc": 6000
      }
    }
  }
}
`
