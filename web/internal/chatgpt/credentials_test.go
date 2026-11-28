package chatgpt

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"briefstudio/web/internal/sessionvault"
)

const vaultHost = "urn:uuid:cbb4d1e4-99ab-4e9f-acc0-c50e6f1a2ad2"

type fixtureVault struct {
	record []byte
	err    error
	saves  int
}

func (v *fixtureVault) Load() ([]byte, error) {
	if len(v.record) == 0 {
		return nil, os.ErrNotExist
	}
	return bytes.Clone(v.record), nil
}

func (v *fixtureVault) Save(record []byte) error {
	v.saves++
	if v.err != nil {
		return v.err
	}
	v.record = bytes.Clone(record)
	return nil
}

func (v *fixtureVault) Delete() error {
	if v.err != nil {
		return v.err
	}
	v.record = nil
	return nil
}

func rememberedFixture(now time.Time, vault *fixtureVault) *Session {
	session := makeSession(now, scopes)
	session.hostID, session.idToken = vaultHost, "fixture_identity"
	session.profile.subject = "fixture_subject"
	session.tokens.expiry = now.Add(-time.Minute)
	session.Remember(vault)
	return session
}

func TestSessionRestoresAfterRestartAndPersistsTokenRotation(t *testing.T) {
	now := time.Now()
	vault := &fixtureVault{}
	rememberedFixture(now, vault)
	c := fakeClient(func() time.Time { return now }, func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case tokenURL:
			if r.ParseForm() != nil || r.Form.Get("grant_type") != "refresh_token" ||
				r.Form.Get("refresh_token") != "fixture_refresh" || r.Form.Get("client_id") != idFixture {
				t.Fatal("refresh did not use the persisted registration")
			}
			return fixtureResponse(200, "application/json",
				`{"access_token":"new_access","refresh_token":"new_refresh","token_type":"Bearer","expires_in":3600}`), nil
		case modelsURL:
			if r.Header.Get("Authorization") != "Bearer new_access" {
				t.Fatal("model listing did not use the renewed access token")
			}
			return catalogResponse(), nil
		default:
			t.Fatal("unexpected provider call")
			return nil, nil
		}
	})
	restored, err := c.RestoreSession(vaultHost, vault)
	if err != nil || restored == nil {
		t.Fatalf("restore failed: %v", err)
	}
	if profile, connected, remembered := restored.Status(); !connected || !remembered || !profile.CanUsePlan {
		t.Fatal("existing account would ask for sign-in")
	}
	if models, err := c.Models(context.Background(), restored); err != nil || len(models) != 1 {
		t.Fatalf("models after restart: %v, %v", len(models), err)
	}
	rotated, err := c.RestoreSession(vaultHost, vault)
	if err != nil || rotated.tokens.refresh != "new_refresh" || rotated.tokens.access != "new_access" {
		t.Fatal("rotated credentials did not survive a second restart")
	}
	if _, err := c.RestoreSession("urn:uuid:95d938be-5d27-4f31-9bdb-f28b3bf70871", vault); !errors.Is(err, sessionvault.ErrUnavailable) {
		t.Fatal("restored credentials for another host")
	}
}

func TestInvalidRefreshErasesCredentialsButReusesRegistration(t *testing.T) {
	now := time.Now()
	vault := &fixtureVault{}
	session := rememberedFixture(now, vault)
	c := fakeClient(func() time.Time { return now }, func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != tokenURL {
			t.Fatal("unexpected provider call")
		}
		return fixtureResponse(401, "application/json", `{"error":{"code":"invalid_grant"}}`), nil
	})
	if _, err := c.Models(context.Background(), session); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("expired account was not recognized: %v", err)
	}
	restored, err := c.RestoreSession(vaultHost, vault)
	if err != nil || restored == nil {
		t.Fatalf("registration lost after refresh expired: %v", err)
	}
	if _, connected, remembered := restored.Status(); connected || remembered ||
		restored.tokens != (tokens{}) || restored.idToken != "" {
		t.Fatal("expired credentials remain after restart")
	}
	attempt, destination, err := c.StartReturning(vaultHost, "http://127.0.0.1:19901/auth/callback", restored)
	if err != nil {
		t.Fatal(err)
	}
	authorize, err := url.Parse(destination)
	if err != nil || authorize.Query().Get("client_id") != idFixture ||
		authorize.Query().Has("agent_name_hint") || authorize.Query().Has("id_token_hint") {
		t.Fatal("did not reuse only the issued registration")
	}
	if _, err := c.Complete(context.Background(), attempt, url.Values{
		"state": {attempt.pending.state}, "code": {"fixture_code"}, "client_id": {"oaiapp_mismatch"},
	}); !errors.Is(err, ErrInvalidCallback) {
		t.Fatalf("substituted client was accepted: %v", err)
	}
}

func TestTemporarySessionCanReconnectAfterExpiry(t *testing.T) {
	now := time.Now()
	session := rememberedFixture(now, &fixtureVault{})
	session.tokens.refresh = ""
	if _, connected, remembered := session.Status(); connected || remembered {
		t.Error("expired temporary grant still reports an active session")
	}
	if session.ScheduleOwner() != "" {
		t.Error("expired temporary grant can still authorize a scheduled brief")
	}
	c := fakeClient(func() time.Time { return now }, func(*http.Request) (*http.Response, error) {
		t.Fatal("preparing a new sign-in made a provider request")
		return nil, nil
	})
	_, destination, err := c.StartReturning(vaultHost, "http://127.0.0.1:19901/auth/callback", session)
	if err != nil {
		t.Fatal(err)
	}
	authorize, err := url.Parse(destination)
	if err != nil || authorize.Query().Get("client_id") != idFixture || authorize.Query().Has("id_token_hint") {
		t.Error("reconnect did not reuse just the verified registration")
	}
}

func TestTemporarySessionUsesRemainingTokenLifetime(t *testing.T) {
	now := time.Now()
	session := makeSession(now, "openid profile resource.invoke "+planScope)
	session.tokens.refresh = ""
	session.tokens.expiry = now.Add(10 * time.Second)
	c := fakeClient(func() time.Time { return now }, func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != modelsURL {
			t.Fatal("unexpected provider call")
		}
		return catalogResponse(), nil
	})
	if models, err := c.Models(context.Background(), session); err != nil || len(models) != 1 {
		t.Fatalf("usable temporary grant was rejected: %v", err)
	}
}

func TestRejectedTemporarySessionNeedsReconnect(t *testing.T) {
	now := time.Now()
	session := makeSession(now, "openid profile resource.invoke "+planScope)
	session.hostID, session.idToken = vaultHost, "fixture_identity"
	session.profile.subject = "fixture_subject"
	session.tokens.refresh = ""
	calls := 0
	c := fakeClient(func() time.Time { return now }, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != modelsURL {
			t.Fatal("rejected temporary grant tried a refresh exchange")
		}
		return fixtureResponse(401, "application/json", `{"error":{"code":"invalid_token"}}`), nil
	})
	if _, err := c.Models(context.Background(), session); !errors.Is(err, ErrSessionExpired) {
		t.Errorf("terminal access rejection was not recognized: %v", err)
	}
	if state := session.State(); state.Connected || state.Remembered || state.Persistence != PersistenceReauthNeeded || session.ScheduleOwner() != "" {
		t.Error("terminal access rejection still reports an active session")
	}
	if _, err := c.Models(context.Background(), session); !errors.Is(err, ErrSessionExpired) || calls != 1 {
		t.Errorf("rejected token was replayed or retried: %v, %d calls", err, calls)
	}
}

func TestSessionStateAndLocalRememberRetry(t *testing.T) {
	now := time.Now()
	var absent *Session
	if state := absent.State(); state.Connected || state.Persistence != PersistenceSignedOut || state.CanRetryRemember {
		t.Fatal("missing session did not report signed out")
	}
	inMemory := makeSession(now, scopes)
	inMemory.Remember(nil)
	if state := inMemory.State(); !state.Connected || state.Persistence != PersistenceVaultUnavailable || state.CanRetryRemember {
		t.Fatal("missing OS vault was not distinguished from a temporary grant")
	}

	vault := &fixtureVault{err: sessionvault.ErrUnavailable}
	session := rememberedFixture(now, vault)
	if state := session.State(); !state.Connected || state.Remembered || state.Persistence != PersistenceVaultUnavailable || !state.CanRetryRemember {
		t.Fatal("failed vault write was not reported as locally retryable")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := session.RetryRemember(ctx); got != PersistenceVaultUnavailable || vault.saves != 1 {
		t.Fatal("cancelled request attempted to save credentials")
	}
	vault.err = nil
	if got := session.RetryRemember(context.Background()); got != PersistenceRemembered || vault.saves != 2 {
		t.Fatal("active session did not persist after the OS vault recovered")
	}
	if state := session.State(); !state.Connected || !state.Remembered || state.Persistence != PersistenceRemembered || state.CanRetryRemember {
		t.Fatal("stored credentials did not report as remembered")
	}
	c := fakeClient(func() time.Time { return now }, func(*http.Request) (*http.Response, error) {
		t.Fatal("restoring an existing record made a provider request")
		return nil, nil
	})
	restored, err := c.RestoreSession(vaultHost, vault)
	if err != nil || restored.State().Persistence != PersistenceRemembered {
		t.Fatalf("remembered credentials did not survive relaunch: %v", err)
	}

	temporary := makeSession(now, "openid profile resource.invoke "+planScope)
	temporary.hostID, temporary.idToken = vaultHost, "fixture_identity"
	temporary.profile.subject = "fixture_subject"
	temporary.tokens.refresh = ""
	temporaryVault := &fixtureVault{}
	temporary.Remember(temporaryVault)
	if state := temporary.State(); !state.Connected || state.Remembered || state.Persistence != PersistenceTemporary || state.CanRetryRemember {
		t.Fatal("temporary grant did not report as session-only")
	}
	if got := temporary.RetryRemember(context.Background()); got != PersistenceTemporary || temporaryVault.saves != 1 {
		t.Fatal("retry attempted to save non-refreshable credentials")
	}
	restored, err = c.RestoreSession(vaultHost, temporaryVault)
	if err != nil || restored == nil || restored.State().Persistence != PersistenceReauthNeeded {
		t.Fatalf("temporary grant did not retain only its registration: %v", err)
	}
}

func TestRejectedRefreshableSessionRenewsOnNextOperation(t *testing.T) {
	now := time.Now()
	session := makeSession(now, scopes)
	var catalogs, rotations int
	c := fakeClient(func() time.Time { return now }, func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case modelsURL:
			catalogs++
			if catalogs == 1 {
				return fixtureResponse(401, "application/json", `{"error":{"code":"invalid_token"}}`), nil
			}
			if r.Header.Get("Authorization") != "Bearer new_access" {
				t.Fatal("next operation reused the rejected access token")
			}
			return catalogResponse(), nil
		case tokenURL:
			rotations++
			return fixtureResponse(200, "application/json", `{"access_token":"new_access","refresh_token":"new_refresh","token_type":"Bearer","expires_in":3600}`), nil
		default:
			t.Fatal("unexpected provider call")
			return nil, nil
		}
	})
	if _, err := c.Models(context.Background(), session); !errors.Is(err, ErrAccessExpired) || catalogs != 1 || rotations != 0 {
		t.Fatalf("first rejection unexpectedly replayed or refreshed: %v", err)
	}
	if !session.State().Connected {
		t.Fatal("refreshable session asked for sign-in prematurely")
	}
	if models, err := c.Models(context.Background(), session); err != nil || len(models) != 1 || catalogs != 2 || rotations != 1 {
		t.Fatalf("next catalog request did not use the refreshed session: %v", err)
	}
}

func TestSignOutClearsVaultEvenWhenRevocationFails(t *testing.T) {
	now := time.Now()
	vault := &fixtureVault{}
	session := rememberedFixture(now, vault)
	c := fakeClient(func() time.Time { return now }, func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != discoveryURL || len(vault.record) != 0 {
			t.Fatal("local credential must be removed before revocation")
		}
		return fixtureResponse(503, "application/json", "{}"), nil
	})
	vault.err = sessionvault.ErrUnavailable
	if err := c.SignOut(context.Background(), session); !errors.Is(err, sessionvault.ErrUnavailable) {
		t.Fatalf("vault delete failure hidden: %v", err)
	}
	if _, connected, remembered := session.Status(); !connected || !remembered {
		t.Fatal("signout falsely disconnected the account while its record remained")
	}
	vault.err = nil
	if err := c.SignOut(context.Background(), session); !errors.Is(err, ErrRevocation) {
		t.Fatalf("missing revocation error: %v", err)
	}
	if restored, err := c.RestoreSession(vaultHost, vault); err != nil || restored != nil {
		t.Fatal("account reappeared after local signout")
	}
}
