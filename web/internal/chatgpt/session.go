package chatgpt

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"briefstudio/web/internal/sessionvault"
)

type tokens struct {
	access, refresh, scope string
	expiry                 time.Time
}

func (r tokenReply) tokens(now time.Time, priorScope string) (tokens, error) {
	scope := priorScope
	if r.Scope != nil {
		scope = *r.Scope
	} else if priorScope == "" {
		return tokens{}, ErrIdentity
	}
	if !strings.EqualFold(r.TokenType, "Bearer") || !oauthToken(r.AccessToken) ||
		r.Expires <= 0 || r.Expires > 86400 ||
		r.RefreshToken != "" && !oauthToken(r.RefreshToken) ||
		(hasScope(scope, "offline_access") || priorScope != "") && r.RefreshToken == "" {
		return tokens{}, ErrIdentity
	}
	return tokens{access: r.AccessToken, refresh: r.RefreshToken, scope: scope,
		expiry: now.Add(time.Duration(r.Expires) * time.Second)}, nil
}

func oauthToken(value string) bool {
	if len(value) == 0 || len(value) > 32<<10 {
		return false
	}
	for _, ch := range value {
		if ch < 33 || ch > 126 {
			return false
		}
	}
	return true
}

// An expired access token can still be usable with a refresh grant.
func (t tokens) usable(now time.Time) bool {
	return t.access != "" && (t.refresh != "" || now.Before(t.expiry))
}

func (t tokens) canUsePlan() bool {
	return hasScope(t.scope, planScope) && hasScope(t.scope, "resource.invoke")
}

func hasScope(scope, wanted string) bool {
	for _, item := range strings.Fields(scope) {
		if item == wanted {
			return true
		}
	}
	return false
}

func (s *Session) requestContext(ctx context.Context) (context.Context, func(), error) {
	if s == nil || s.lifetime == nil || s.lifetime.Err() != nil {
		return nil, nil, ErrSignedOut
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.lifetime, cancel)
	return ctx, func() { stop(); cancel() }, nil
}

func (c *Client) accessToken(ctx context.Context, s *Session) (string, error) {
	// Hold the lock while the provider rotates the refresh token. Waiters
	// use the new credentials instead of racing with the old token.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lifetime.Err() != nil {
		return "", ErrSignedOut
	}
	if err := ctx.Err(); err != nil {
		return "", transportError(err)
	}
	now := c.now()
	if !s.tokens.usable(now) {
		if s.tokens.access != "" {
			s.expireLocked()
		}
		return "", ErrSessionExpired
	}
	if !s.tokens.canUsePlan() {
		return "", ErrPlanNotGranted
	}
	if s.tokens.refresh == "" || now.Add(30*time.Second).Before(s.tokens.expiry) {
		return s.tokens.access, nil
	}

	var reply tokenReply
	err := c.readJSON(ctx, http.MethodPost, tokenURL, "", url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {s.clientID},
		"refresh_token": {s.tokens.refresh},
		"resource":      {resource},
	}, &reply)
	if err != nil {
		var response *providerError
		if errors.As(err, &response) && (expiredRefresh(response.code) || response.status == http.StatusUnauthorized) {
			s.expireLocked()
			return "", ErrSessionExpired
		}
		return "", publicError(err)
	}
	renewed, err := reply.tokens(c.now(), s.tokens.scope)
	if err != nil {
		return "", err
	}
	s.tokens = renewed
	s.profile.CanUsePlan = renewed.canUsePlan()
	s.persistLocked()
	if !s.profile.CanUsePlan {
		s.models = nil
		return "", ErrPlanNotGranted
	}
	return renewed.access, nil
}

// A resource can reject an access token before its advertised expiry. When
// possible, renew on the next operation instead of replaying the request.
func (c *Client) resourceError(s *Session, access string, err error) error {
	public := publicError(err)
	if !errors.Is(public, ErrSessionExpired) {
		return public
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lifetime.Err() != nil {
		return ErrSignedOut
	}
	if s.tokens.access == "" {
		return ErrSessionExpired
	}
	if s.tokens.access == access {
		if s.tokens.refresh == "" {
			s.expireLocked()
			return ErrSessionExpired
		}
		s.tokens.expiry = c.now()
		s.persistLocked()
	}
	return ErrAccessExpired
}

// expireLocked removes exhausted credentials while retaining the registration
// that lets the same account authorize again.
func (s *Session) expireLocked() {
	s.tokens = tokens{}
	s.idToken = ""
	s.models = nil
	s.profile.CanUsePlan = false
	s.persistLocked()
}

func expiredRefresh(code string) bool {
	switch code {
	case "invalid_grant", "invalid_refresh_token", "token_expired", "refresh_token_expired",
		"refresh_token_invalidated", "refresh_token_reused":
		return true
	}
	return false
}

func (c *Client) SignOut(ctx context.Context, s *Session) error {
	if s == nil || s.cancel == nil {
		return nil
	}
	s.mu.Lock()
	// Let an exchange in progress finish rotating before choosing which
	// refresh token to revoke. No subsequent request can use this session.
	if s.store != nil && s.store.Delete() != nil {
		s.mu.Unlock()
		return sessionvault.ErrUnavailable
	}
	s.cancel()
	refresh, id := s.tokens.refresh, s.clientID
	s.tokens = tokens{}
	s.profile = Profile{}
	s.models = nil
	s.clientID = ""
	s.idToken, s.hostID, s.remembered = "", "", false
	s.mu.Unlock()
	if refresh == "" {
		return nil
	}

	var discovery struct {
		Issuer     string `json:"issuer"`
		Revocation string `json:"revocation_endpoint"`
	}
	err := c.readJSON(ctx, http.MethodGet, discoveryURL, "", nil, &discovery)
	if err != nil || discovery.Issuer != issuer || discovery.Revocation != revocationURL {
		return ErrRevocation
	}
	form := url.Values{"token": {refresh}, "token_type_hint": {"refresh_token"}, "client_id": {id}}
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(200 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ErrRevocation
			case <-timer.C:
			}
		}
		reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		response, err := c.request(reqCtx, http.MethodPost, revocationURL, "",
			"application/x-www-form-urlencoded", "application/json", strings.NewReader(form.Encode()))
		if err != nil {
			cancel()
			if !errors.Is(err, ErrNetwork) && !errors.Is(err, ErrTimeout) {
				break
			}
			continue
		}
		response.Body.Close()
		cancel()
		if response.StatusCode == http.StatusOK {
			return nil
		}
		if response.StatusCode < 500 {
			break
		}
	}
	return ErrRevocation
}
