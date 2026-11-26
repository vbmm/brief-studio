package chatgpt

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
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
	if s.tokens.access == "" {
		return "", ErrSessionExpired
	}
	if !s.tokens.canUsePlan() {
		return "", ErrPlanNotGranted
	}
	if c.now().Add(30 * time.Second).Before(s.tokens.expiry) {
		return s.tokens.access, nil
	}
	if s.tokens.refresh == "" {
		return "", ErrSessionExpired
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
		if errors.As(err, &response) && expiredRefresh(response.code) {
			s.tokens = tokens{}
			s.models = nil
			s.profile = Profile{}
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
	if !s.profile.CanUsePlan {
		s.models = nil
		return "", ErrPlanNotGranted
	}
	return renewed.access, nil
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
	s.cancel()
	refresh, id := s.tokens.refresh, s.clientID
	s.tokens = tokens{}
	s.profile = Profile{}
	s.models = nil
	s.clientID = ""
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
