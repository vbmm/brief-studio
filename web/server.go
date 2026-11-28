package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"sync"
	"time"

	"briefstudio/web/internal/chatgpt"
	"briefstudio/web/internal/engine"
	"briefstudio/web/internal/schedule"
	"briefstudio/web/internal/sessionvault"
	"briefstudio/web/internal/sources"
)

type studioServer struct {
	origin, host, bootKey, browserKey, cookieName string
	assets                                        fs.FS
	library                                       *library
	client                                        *chatgpt.Client
	sessionStore                                  sessionvault.Store
	schedule                                      *schedule.Store
	storageWarning                                string
	sourceSettings                                *sourceStore
	fetchSource                                   func(context.Context, sources.Provider, sources.Settings, time.Time) ([]sources.Headline, error)
	fetchPublicGEX                                func(context.Context, time.Time) (*sources.MarketSnapshot, error)
	refreshBird                                   func(context.Context) (sources.XSnapshot, error)

	mu        sync.Mutex
	pending   *chatgpt.Attempt
	session   *chatgpt.Session
	models    []chatgpt.Model
	authError string
	busy      bool
}

func (s *studioServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.asset("index.html", "text/html; charset=utf-8"))
	mux.HandleFunc("GET /app.js", s.asset("app.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /style.css", s.asset("style.css", "text/css; charset=utf-8"))
	mux.HandleFunc("POST /api/bootstrap", s.bootstrap)
	mux.HandleFunc("POST /auth/start", s.withSession(false, s.startAuth))
	mux.HandleFunc("GET /auth/callback", s.withSession(false, s.finishAuth))
	mux.HandleFunc("GET /api/state", s.withSession(true, s.state))
	mux.HandleFunc("GET /api/models", s.withSession(true, s.listModels))
	mux.HandleFunc("POST /api/account/remember", s.withSession(true, s.rememberAccount))
	mux.HandleFunc("POST /api/signout", s.withSession(true, s.signOut))
	mux.HandleFunc("POST /api/generate", s.withSession(true, s.generate))
	mux.HandleFunc("GET /api/schedule", s.withSession(true, s.scheduleState))
	mux.HandleFunc("POST /api/schedule", s.withSession(true, s.changeSchedule))
	mux.HandleFunc("GET /api/update", s.withSession(true, checkUpdate))
	mux.HandleFunc("GET /api/sources", s.withSession(true, s.sourceStatus))
	mux.HandleFunc("GET /api/sources/market", s.withSession(true, s.marketSourceStatus))
	mux.HandleFunc("POST /api/sources/market", s.withSession(true, s.changeMarketSource))
	mux.HandleFunc("GET /api/sources/market/template", s.withSession(false, marketSourceTemplate))
	mux.HandleFunc("POST /api/sources/market/public/test", s.withSession(true, s.testPublicGEX))
	mux.HandleFunc("POST /api/sources/x/snapshot", s.withSession(true, s.changeXSnapshot))
	mux.HandleFunc("POST /api/sources/x/bird", s.withSession(true, s.birdSnapshot))
	mux.HandleFunc("POST /api/sources/{provider}", s.withSession(true, s.changeSource))
	mux.HandleFunc("POST /api/sources/{provider}/test", s.withSession(true, s.testSource))
	mux.HandleFunc("GET /api/sample", s.withSession(true, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, engine.Sample())
	}))
	mux.HandleFunc("GET /api/briefs", s.withSession(true, func(w http.ResponseWriter, _ *http.Request) {
		entries, err := s.library.list()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "La bibliothèque locale est indisponible.")
			return
		}
		writeJSON(w, http.StatusOK, entries)
	}))
	mux.HandleFunc("GET /api/briefs/{id}", s.withSession(true, s.readBrief))
	mux.HandleFunc("GET /api/briefs/{id}/export", s.withSession(false, s.exportBrief))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != s.host {
			http.Error(w, "Adresse locale requise", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; form-action 'self' https://auth.openai.com; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	})
}

func (s *studioServer) withSession(api bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(s.cookieName)
		if err != nil || !sameSecret(cookie.Value, s.browserKey) {
			writeError(w, http.StatusUnauthorized, "Ouvre le lien affiché par Brief Studio pour accéder à cette session locale.")
			return
		}
		if api && r.Header.Get("X-BriefStudio-Request") != "1" {
			writeError(w, http.StatusForbidden, "Requête locale requise.")
			return
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" && r.URL.Path != "/auth/callback" {
			writeError(w, http.StatusForbidden, "Accès local requis.")
			return
		}
		if r.Method == http.MethodPost && r.Header.Get("Origin") != s.origin {
			writeError(w, http.StatusForbidden, "Origine locale requise.")
			return
		}
		next(w, r)
	}
}

func (s *studioServer) bootstrap(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != s.origin || r.Header.Get("X-BriefStudio-Request") != "1" {
		writeError(w, http.StatusForbidden, "Ouverture locale requise.")
		return
	}
	var req struct {
		Key string `json:"key"`
	}
	if readJSON(w, r, &req) != nil || !sameSecret(req.Key, s.bootKey) {
		writeError(w, http.StatusUnauthorized, "Ce lien a expiré. Ouvre celui affiché par l’app en cours.")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: s.cookieName, Value: s.browserKey, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *studioServer) startAuth(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		writeError(w, http.StatusConflict, "Une opération est déjà en cours.")
		return
	}
	if _, connected, _ := s.session.Status(); connected {
		s.mu.Unlock()
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	attempt, authorize, err := s.client.StartReturning(s.library.hostID, s.origin+"/auth/callback", s.session)
	if err == nil {
		s.pending = &attempt
		s.authError = ""
	} else {
		s.authError = "Impossible de préparer la connexion à ChatGPT."
	}
	s.mu.Unlock()
	if err != nil {
		http.Redirect(w, r, "/?auth=error", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, authorize, http.StatusSeeOther)
}

func (s *studioServer) finishAuth(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.pending == nil || !s.pending.MatchesState(r.URL.Query()) || s.busy {
		s.mu.Unlock()
		http.Error(w, "Demande inconnue ou expirée. Reviens à Brief Studio et lance une nouvelle connexion.", http.StatusBadRequest)
		return
	}
	attempt := *s.pending
	s.pending = nil
	s.busy = true
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 55*time.Second)
	defer cancel()
	session, err := s.client.Complete(ctx, attempt, r.URL.Query())
	if err == nil {
		session.Remember(s.sessionStore)
	}
	s.mu.Lock()
	s.busy = false
	if err == nil {
		s.session = session
		s.models = nil
		s.authError = ""
	} else {
		s.authError = err.Error()
	}
	s.mu.Unlock()
	if err != nil {
		http.Redirect(w, r, "/?auth=error", http.StatusSeeOther)
	} else {
		http.Redirect(w, r, "/?auth=ok", http.StatusSeeOther)
	}
}

func (s *studioServer) state(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	session, authError := s.session, s.authError
	s.mu.Unlock()
	account := session.State()
	response := struct {
		Connected      bool                      `json:"connected"`
		HasAccount     bool                      `json:"hasAccount"`
		Remembered     bool                      `json:"remembered"`
		Persistence    chatgpt.PersistenceStatus `json:"persistence"`
		CanRetry       bool                      `json:"canRetryRemember"`
		MarketClosed   bool                      `json:"marketClosed"`
		Profile        chatgpt.Profile           `json:"profile"`
		AuthError      string                    `json:"authError,omitempty"`
		StorageWarning string                    `json:"storageWarning,omitempty"`
		Version        string                    `json:"version"`
	}{
		Connected: account.Connected, HasAccount: session != nil,
		Remembered: account.Remembered, Persistence: account.Persistence, CanRetry: account.CanRetryRemember,
		MarketClosed: engine.MarketClosed(time.Now()), Profile: account.Profile, AuthError: authError,
		StorageWarning: s.storageWarning, Version: version,
	}
	writeJSON(w, http.StatusOK, response)
}

type operationAccess uint8

const (
	localOperation operationAccess = iota
	accountOperation
	modelOperation
)

func (s *studioServer) takeOperation(w http.ResponseWriter, access operationAccess) (*chatgpt.Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy {
		writeError(w, http.StatusConflict, "Une opération est déjà en cours.")
		return nil, false
	}
	if access != localOperation && s.session == nil {
		writeError(w, http.StatusUnauthorized, "Connecte-toi à ChatGPT pour continuer.")
		return nil, false
	}
	if access == modelOperation {
		profile, connected, _ := s.session.Status()
		if !connected {
			writeError(w, http.StatusUnauthorized, chatgpt.ErrSessionExpired.Error())
			return nil, false
		}
		if !profile.CanUsePlan {
			writeError(w, http.StatusForbidden, "L’utilisation du forfait ChatGPT n’est pas autorisée pour cette session.")
			return nil, false
		}
	}
	s.busy = true
	return s.session, true
}

func (s *studioServer) releaseOperation() {
	s.mu.Lock()
	s.busy = false
	s.mu.Unlock()
}

func (s *studioServer) listModels(w http.ResponseWriter, r *http.Request) {
	session, ok := s.takeOperation(w, modelOperation)
	if !ok {
		return
	}
	defer s.releaseOperation()
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	models, err := s.client.Models(ctx, session)
	if err != nil {
		writeModelError(w, err)
		return
	}
	if models == nil {
		models = []chatgpt.Model{}
	}
	s.mu.Lock()
	s.models = models
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, models)
}

func (s *studioServer) rememberAccount(w http.ResponseWriter, r *http.Request) {
	if readJSON(w, r, &struct{}{}) != nil {
		writeError(w, http.StatusBadRequest, "Demande de sauvegarde invalide.")
		return
	}
	session, ok := s.takeOperation(w, accountOperation)
	if !ok {
		return
	}
	defer s.releaseOperation()
	session.RetryRemember(r.Context())
	s.state(w, r)
}

func (s *studioServer) signOut(w http.ResponseWriter, r *http.Request) {
	session, ok := s.takeOperation(w, accountOperation)
	if !ok {
		return
	}
	defer s.releaseOperation()
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	err := s.client.SignOut(ctx, session)
	if err == nil || errors.Is(err, chatgpt.ErrRevocation) {
		if s.schedule != nil {
			// The account binding independently prevents execution if the
			// schedule file cannot be updated during sign-out.
			_ = s.schedule.Update(schedule.Config{}, time.Now())
		}
		s.mu.Lock()
		s.session, s.pending, s.models = nil, nil, nil
		s.authError = ""
		s.mu.Unlock()
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, struct {
		OK bool `json:"ok"`
	}{true})
}

type briefRequest struct {
	Model       string         `json:"model"`
	Note        string         `json:"note"`
	SourceURL   string         `json:"sourceURL"`
	GEXMode     engine.GEXMode `json:"gexMode"`
	MarketRecap bool           `json:"marketRecap"`
}

var errArchiveSave = errors.New("Le brief n’a pas pu être enregistré sur cet ordinateur.")

func (s *studioServer) generate(w http.ResponseWriter, r *http.Request) {
	var req briefRequest
	if readJSON(w, r, &req) != nil {
		writeError(w, http.StatusBadRequest, "Demande de brief invalide.")
		return
	}
	if !req.GEXMode.Valid() {
		writeError(w, http.StatusBadRequest, "Choisis un mode GEX valide.")
		return
	}
	access := modelOperation
	if req.MarketRecap {
		access = localOperation
		if !engine.MarketClosed(time.Now()) {
			writeError(w, http.StatusConflict, engine.ErrMarketOpen.Error())
			return
		}
	}
	session, ok := s.takeOperation(w, access)
	if !ok {
		return
	}
	defer s.releaseOperation()
	if !req.MarketRecap {
		s.mu.Lock()
		allowed := modelAvailable(s.models, req.Model)
		s.mu.Unlock()
		if !allowed {
			writeError(w, http.StatusBadRequest, "Choisis un modèle disponible pour ton compte.")
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 145*time.Second)
	defer cancel()
	id, doc, err := s.buildAndSaveBrief(ctx, session, req)
	if err != nil {
		switch {
		case errors.Is(err, engine.ErrMarketOpen):
			writeError(w, http.StatusConflict, err.Error())
		case errors.Is(err, engine.ErrGEXUnavailable):
			writeError(w, http.StatusUnprocessableEntity, err.Error())
		case errors.Is(err, errArchiveSave):
			writeError(w, http.StatusInternalServerError, err.Error())
		default:
			writeModelError(w, err)
		}
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		ID       string          `json:"id"`
		Document engine.Document `json:"document"`
	}{id, doc})
}

func modelAvailable(models []chatgpt.Model, selected string) bool {
	for _, model := range models {
		if model.ID == selected {
			return true
		}
	}
	return false
}

func (s *studioServer) buildAndSaveBrief(ctx context.Context, session *chatgpt.Session, req briefRequest) (string, engine.Document, error) {
	cutoff := time.Now()
	market, marketErr := s.sourceSettings.readMarketSnapshot(cutoff)
	if marketErr != nil && req.GEXMode == engine.WithGEX {
		return "", engine.Document{}, errors.Join(engine.ErrGEXUnavailable, marketErr)
	}
	input := engine.GenerationSources{
		News: s.gatherOptionalNews, Market: market, MarketReadFailed: marketErr != nil, GEXMode: req.GEXMode,
	}
	if req.GEXMode == engine.PublicGEX {
		// A missing public reading is displayed locally and never gates the brief.
		input.PublicGEX, _ = s.readPublicGEX(ctx, cutoff)
	}
	var analyze func(context.Context, string) (string, error)
	if !req.MarketRecap {
		analyze = func(ctx context.Context, input string) (string, error) {
			return s.client.Respond(ctx, session, req.Model, input)
		}
	}
	doc, err := engine.GenerateWithSources(ctx, req.Note, req.SourceURL, analyze, input)
	if err != nil {
		return "", engine.Document{}, err
	}
	// Linearize the final write with a pause that cancels a scheduled run.
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", engine.Document{}, err
	}
	id, err := s.library.save(doc)
	if err != nil {
		return "", engine.Document{}, errArchiveSave
	}
	return id, doc, nil
}

func writeModelError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	if errors.Is(err, chatgpt.ErrSessionExpired) || errors.Is(err, chatgpt.ErrSignedOut) {
		status = http.StatusUnauthorized
	}
	writeError(w, status, err.Error())
}

func (s *studioServer) asset(name, mime string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := fs.ReadFile(s.assets, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", mime)
		_, _ = w.Write(body)
	}
}

func sameSecret(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

func readJSON(w http.ResponseWriter, r *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("fin JSON invalide")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, struct {
		Error string `json:"error"`
	}{message})
}
