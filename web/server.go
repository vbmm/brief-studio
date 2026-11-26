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
	"briefstudio/web/internal/sources"
)

type studioServer struct {
	origin, host, bootKey, browserKey, cookieName string
	assets                                        fs.FS
	library                                       *library
	client                                        *chatgpt.Client
	sourceSettings                                *sourceStore
	fetchSource                                   func(context.Context, sources.Provider, sources.Settings, time.Time) ([]sources.Headline, error)

	mu        sync.Mutex
	pending   *chatgpt.Attempt
	session   *chatgpt.Session
	profile   chatgpt.Profile
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
	mux.HandleFunc("POST /api/signout", s.withSession(true, s.signOut))
	mux.HandleFunc("POST /api/generate", s.withSession(true, s.generate))
	mux.HandleFunc("GET /api/update", s.withSession(true, checkUpdate))
	mux.HandleFunc("GET /api/sources", s.withSession(true, s.sourceStatus))
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
	attempt, authorize, err := s.client.Start(s.library.hostID, s.origin+"/auth/callback")
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
	s.mu.Lock()
	s.busy = false
	if err == nil {
		s.session, s.profile = session, session.Profile()
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
	response := struct {
		Connected bool            `json:"connected"`
		Profile   chatgpt.Profile `json:"profile"`
		AuthError string          `json:"authError,omitempty"`
		Version   string          `json:"version"`
	}{s.session != nil, s.profile, s.authError, version}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, response)
}

func (s *studioServer) takeOperation(w http.ResponseWriter, requirePlan bool) (*chatgpt.Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy {
		writeError(w, http.StatusConflict, "Une opération est déjà en cours.")
		return nil, false
	}
	if s.session == nil {
		writeError(w, http.StatusUnauthorized, "Connecte-toi à ChatGPT pour continuer.")
		return nil, false
	}
	if requirePlan && !s.profile.CanUsePlan {
		writeError(w, http.StatusForbidden, "L’utilisation du forfait ChatGPT n’est pas autorisée pour cette session.")
		return nil, false
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
	session, ok := s.takeOperation(w, true)
	if !ok {
		return
	}
	defer s.releaseOperation()
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	models, err := s.client.Models(ctx, session)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
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

func (s *studioServer) signOut(w http.ResponseWriter, r *http.Request) {
	session, ok := s.takeOperation(w, false)
	if !ok {
		return
	}
	defer s.releaseOperation()
	s.mu.Lock()
	s.session, s.pending, s.models = nil, nil, nil
	s.profile, s.authError = chatgpt.Profile{}, ""
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	if err := s.client.SignOut(ctx, session); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, struct {
		OK bool `json:"ok"`
	}{true})
}

func (s *studioServer) generate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model     string `json:"model"`
		Note      string `json:"note"`
		SourceURL string `json:"sourceURL"`
	}
	if readJSON(w, r, &req) != nil {
		writeError(w, http.StatusBadRequest, "Demande de brief invalide.")
		return
	}
	session, ok := s.takeOperation(w, true)
	if !ok {
		return
	}
	defer s.releaseOperation()
	s.mu.Lock()
	allowed := false
	for _, model := range s.models {
		if model.ID == req.Model {
			allowed = true
			break
		}
	}
	s.mu.Unlock()
	if !allowed {
		writeError(w, http.StatusBadRequest, "Choisis un modèle disponible pour ton compte.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 145*time.Second)
	defer cancel()
	doc, err := engine.GenerateWithNews(ctx, req.Note, req.SourceURL, func(ctx context.Context, input string) (string, error) {
		return s.client.Respond(ctx, session, req.Model, input)
	}, s.gatherOptionalNews)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	id, err := s.library.save(doc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Le brief n’a pas pu être enregistré sur cet ordinateur.")
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		ID       string          `json:"id"`
		Document engine.Document `json:"document"`
	}{id, doc})
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
