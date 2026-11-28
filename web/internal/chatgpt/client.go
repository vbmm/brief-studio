// Package chatgpt implements the Sign in with ChatGPT flow for a local app.
// OAuth attempts stay in server memory; a session may use local OS storage.
package chatgpt

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"briefstudio/web/internal/sessionvault"
)

const (
	issuer          = "https://auth.openai.com"
	authorizeURL    = issuer + "/api/accounts/authorize"
	tokenURL        = issuer + "/api/accounts/oauth/token"
	revocationURL   = issuer + "/api/accounts/oauth/revoke"
	discoveryURL    = issuer + "/.well-known/openid-configuration"
	jwksURL         = issuer + "/.well-known/jwks.json"
	resource        = "https://api.openai.com/v1"
	modelsURL       = resource + "/models"
	responsesURL    = resource + "/responses"
	scopes          = "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"
	planScope       = "chatgpt.tokens.use.direct"
	attemptLifetime = 10 * time.Minute
	requestTimeout  = 25 * time.Second
	streamTimeout   = 3 * time.Minute
)

var (
	ErrInvalidCallback = errors.New("Le retour de connexion ChatGPT est invalide.")
	ErrExpiredAttempt  = errors.New("La connexion a expiré. Recommence depuis Brief Studio.")
	ErrDeclined        = errors.New("La connexion ChatGPT a été refusée.")
	ErrIdentity        = errors.New("Impossible de vérifier l’identité ChatGPT.")
	ErrSignedOut       = errors.New("Connecte ton compte ChatGPT.")
	ErrPlanNotGranted  = errors.New("Ce compte n’a pas autorisé l’utilisation de son forfait ChatGPT.")
	ErrPlanLimit       = errors.New("La limite d’utilisation de Brief Studio ou de ton forfait ChatGPT est atteinte.")
	ErrSessionExpired  = errors.New("La session ChatGPT a expiré. Reconnecte-toi.")
	ErrAccessExpired   = errors.New("L’accès à ChatGPT a expiré. Réessaie pour renouveler la session.")
	ErrModel           = errors.New("Ce modèle ChatGPT est indisponible pour ce compte.")
	ErrIncomplete      = errors.New("La réponse de ChatGPT est incomplète.")
	ErrRevocation      = errors.New("Session effacée dans Brief Studio. OpenAI n’a pas confirmé la révocation.")
	ErrNetwork         = errors.New("Impossible de joindre OpenAI. Vérifie ta connexion puis réessaie.")
	ErrTimeout         = errors.New("OpenAI n’a pas répondu à temps. Réessaie.")
)

var (
	uuidHost = regexp.MustCompile(`(?i)^urn:uuid:[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	clientID = regexp.MustCompile(`^oaiapp_[a-zA-Z0-9_-]{1,248}$`)
)

type Client struct {
	http *http.Client
	now  func() time.Time
}

func New() *Client {
	// A dedicated transport uses the direct provider connection, without
	// inheriting redirects or a proxy from the local environment.
	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: requestTimeout,
		IdleConnTimeout:       60 * time.Second,
		MaxIdleConns:          8,
	}
	return &Client{
		http: &http.Client{
			Transport: transport,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		now: time.Now,
	}
}

// Attempt can be copied for storage, but each attempt can be completed only once.
type Attempt struct {
	pending *authorization
}

type authorization struct {
	state, nonce, verifier, redirect string
	hostID, clientID, subject        string
	expires                          time.Time
	used                             atomic.Bool
}

// Start makes an authorization attempt and its URL. The app must already be
// listening on the exact callback URL. Store the attempt until the callback.
func (c *Client) Start(hostID, callbackURL string) (Attempt, string, error) {
	return c.StartReturning(hostID, callbackURL, nil)
}

// StartReturning reuses the verified registration belonging to this host.
func (c *Client) StartReturning(hostID, callbackURL string, session *Session) (Attempt, string, error) {
	if !uuidHost.MatchString(hostID) || !validCallback(callbackURL) {
		return Attempt{}, "", errors.New("Les paramètres de connexion locale sont invalides.")
	}
	pending := &authorization{redirect: callbackURL, hostID: hostID, expires: c.now().Add(attemptLifetime)}
	var hint string
	if session != nil {
		session.mu.Lock()
		if session.hostID == hostID && clientID.MatchString(session.clientID) && validSubject(session.profile.subject) {
			pending.clientID, pending.subject = session.clientID, session.profile.subject
			if session.lifetime != nil && session.lifetime.Err() == nil && session.tokens.usable(c.now()) {
				hint = session.idToken
			}
		}
		session.mu.Unlock()
	}
	for _, secret := range []*string{&pending.state, &pending.nonce, &pending.verifier} {
		var bytes [32]byte
		if _, err := rand.Read(bytes[:]); err != nil {
			return Attempt{}, "", errors.New("Impossible de préparer la connexion sécurisée.")
		}
		*secret = base64.RawURLEncoding.EncodeToString(bytes[:])
	}
	challenge := sha256.Sum256([]byte(pending.verifier))
	query := url.Values{
		"client_id":             {"dynamic_agent_client"},
		"agent_name_hint":       {"Brief Studio"},
		"ext_agent_host_id":     {hostID},
		"response_type":         {"code"},
		"redirect_uri":          {pending.redirect},
		"scope":                 {scopes},
		"resource":              {resource},
		"state":                 {pending.state},
		"nonce":                 {pending.nonce},
		"code_challenge_method": {"S256"},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
	}
	if pending.clientID != "" {
		query.Set("client_id", pending.clientID)
		query.Del("agent_name_hint")
		if hint != "" {
			query.Set("id_token_hint", hint)
		}
	}
	return Attempt{pending}, authorizeURL + "?" + query.Encode(), nil
}

func validCallback(value string) bool {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Opaque != "" ||
		u.EscapedPath() != "/auth/callback" || u.ForceQuery || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil || host != "127.0.0.1" {
		return false
	}
	p, err := strconv.ParseUint(port, 10, 16)
	return err == nil && p > 0 && value == "http://127.0.0.1:"+strconv.FormatUint(p, 10)+"/auth/callback"
}

// MatchesState tells the caller whether a callback belongs to the attempt.
// Complete still checks expiry, exact state and single use independently.
func (a Attempt) MatchesState(query url.Values) bool {
	state, ok := one(query, "state")
	return a.pending != nil && ok && subtle.ConstantTimeCompare([]byte(state), []byte(a.pending.state)) == 1
}

// Complete validates and consumes an attempt, exchanges the provider code,
// and returns an account session only after verifying the signed identity.
func (c *Client) Complete(ctx context.Context, attempt Attempt, query url.Values) (*Session, error) {
	pending := attempt.pending
	if pending == nil || !c.now().Before(pending.expires) || pending.used.Load() {
		return nil, ErrExpiredAttempt
	}
	if !attempt.MatchesState(query) {
		return nil, ErrInvalidCallback
	}
	if !pending.used.CompareAndSwap(false, true) {
		return nil, ErrExpiredAttempt
	}

	if failure, present := query["error"]; present {
		if len(failure) == 1 && failure[0] == "access_denied" {
			return nil, ErrDeclined
		}
		return nil, ErrInvalidCallback
	}
	code, validCode := one(query, "code")
	id, validID := one(query, "client_id")
	if pending.clientID != "" {
		if _, supplied := query["client_id"]; !supplied {
			id, validID = pending.clientID, true
		}
		validID = validID && id == pending.clientID
	}
	if !validCode || len(code) > 4096 || !validID || !clientID.MatchString(id) {
		return nil, ErrInvalidCallback
	}

	var reply tokenReply
	err := c.readJSON(ctx, http.MethodPost, tokenURL, "", url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {id},
		"code":          {code},
		"code_verifier": {pending.verifier},
		"redirect_uri":  {pending.redirect},
		"resource":      {resource},
	}, &reply)
	if err != nil {
		return nil, publicError(err)
	}

	tokens, err := reply.tokens(c.now(), "")
	if err != nil {
		return nil, err
	}
	var keys signingKeys
	if err := c.readJSON(ctx, http.MethodGet, jwksURL, "", nil, &keys); err != nil {
		return nil, publicError(err)
	}
	profile, err := verifyIdentity(reply.IDToken, id, pending.nonce, keys, c.now())
	if err != nil {
		return nil, err
	}
	if pending.subject != "" && profile.subject != pending.subject {
		return nil, ErrIdentity
	}

	profile.CanUsePlan = tokens.canUsePlan()
	lifetime, cancel := context.WithCancel(context.Background())
	return &Session{
		profile: profile, hostID: pending.hostID, clientID: id, idToken: reply.IDToken,
		tokens: tokens, now: c.now, lifetime: lifetime, cancel: cancel,
	}, nil
}

func one(query url.Values, key string) (string, bool) {
	values := query[key]
	if len(values) != 1 || values[0] == "" {
		return "", false
	}
	return values[0], true
}

type Profile struct {
	Name       string `json:"name"`
	Email      string `json:"email"`
	CanUsePlan bool   `json:"canUsePlan"`
	subject    string
}

type Model struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

// Session owns the active tokens and serializes updates to its optional vault.
type Session struct {
	mu         sync.Mutex
	profile    Profile
	clientID   string
	hostID     string
	idToken    string
	tokens     tokens
	models     map[string]bool
	now        func() time.Time
	lifetime   context.Context
	cancel     context.CancelFunc
	store      sessionvault.Store
	remembered bool
}

func (s *Session) Profile() Profile {
	if s == nil {
		return Profile{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.profile
}

type tokenReply struct {
	AccessToken  string  `json:"access_token"`
	RefreshToken string  `json:"refresh_token"`
	IDToken      string  `json:"id_token"`
	TokenType    string  `json:"token_type"`
	Expires      int64   `json:"expires_in"`
	Scope        *string `json:"scope"`
}
