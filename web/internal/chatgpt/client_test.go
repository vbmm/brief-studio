package chatgpt

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	hostFixture     = "urn:uuid:29558a8c-0430-48ac-8711-9db71bd68c0c"
	redirectFixture = "http://127.0.0.1:36916/auth/callback"
	idFixture       = "oaiapp_fixture"
	deltaFixture    = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Séance \"}"
	finalFixture    = "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (fn roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func fakeClient(now func() time.Time, serve roundTrip) *Client {
	return &Client{now: now, http: &http.Client{Transport: serve}}
}

func fixtureResponse(status int, media, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)),
		Header: http.Header{"Content-Type": {media}}}
}

func jsonResponse(t *testing.T, value any) *http.Response {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return fixtureResponse(200, "application/json", string(body))
}

func makeAttempt(t *testing.T, c *Client) (Attempt, url.Values) {
	t.Helper()
	attempt, address, err := c.Start(hostFixture, redirectFixture)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || u.Host != "auth.openai.com" || u.Path != "/api/accounts/authorize" {
		t.Fatal("wrong provider authorization origin")
	}
	return attempt, u.Query()
}

func TestStartLocalCallbackAndBoundPKCE(t *testing.T) {
	c := New()
	attempt, query := makeAttempt(t, c)
	hash := sha256.Sum256([]byte(attempt.pending.verifier))
	for key, expected := range map[string]string{
		"scope": scopes, "redirect_uri": redirectFixture, "resource": resource,
		"client_id": "dynamic_agent_client", "agent_name_hint": "Brief Studio", "ext_agent_host_id": hostFixture,
		"code_challenge_method": "S256", "code_challenge": base64.RawURLEncoding.EncodeToString(hash[:]),
	} {
		if query.Get(key) != expected {
			t.Fatalf("OAuth parameter %s is missing or incorrect", key)
		}
	}
	if query.Get("nonce") == "" || query.Get("state") == query.Get("nonce") || query.Get("code_verifier") != "" ||
		query.Get("id_token_hint") != "" || !attempt.MatchesState(query) {
		t.Fatal("authorization values are unsafe")
	}
	for _, bad := range []string{
		"https://127.0.0.1:1234/auth/callback", "http://localhost:1234/auth/callback",
		"http://127.0.0.1:0/auth/callback", "http://127.0.0.1:1234/auth/callback?x=1",
		"http://127.0.0.1:1234/auth/callback#", "http://127.0.0.1:65536/auth/callback",
	} {
		if _, _, err := c.Start(hostFixture, bad); err == nil {
			t.Fatal("non-canonical callback was accepted")
		}
	}
	if _, _, err := c.Start("external-account", redirectFixture); err == nil {
		t.Fatal("host identifier was not validated")
	}
}

func TestCallbackRejectsWrongStateExpiredAndDeclined(t *testing.T) {
	now := time.Now()
	c := fakeClient(func() time.Time { return now }, func(*http.Request) (*http.Response, error) {
		t.Fatal("bad authorization made a network request")
		return nil, nil
	})
	attempt, start := makeAttempt(t, c)
	state := start.Get("state")
	for _, query := range []url.Values{
		{"state": {"wrong"}, "code": {"fixture"}},
		{"state": {state, state}, "code": {"fixture"}},
	} {
		if attempt.MatchesState(query) {
			t.Fatal("invalid state matched")
		}
		if _, err := c.Complete(context.Background(), attempt, query); !errors.Is(err, ErrInvalidCallback) {
			t.Fatalf("invalid state: %v", err)
		}
	}
	declined := url.Values{"state": {state}, "error": {"access_denied"}}
	if _, err := c.Complete(context.Background(), attempt, declined); !errors.Is(err, ErrDeclined) {
		t.Fatalf("declined: %v", err)
	}
	if _, err := c.Complete(context.Background(), attempt, declined); !errors.Is(err, ErrExpiredAttempt) {
		t.Fatalf("replayed: %v", err)
	}
	expired, start := makeAttempt(t, c)
	now = now.Add(attemptLifetime)
	if _, err := c.Complete(context.Background(), expired, start); !errors.Is(err, ErrExpiredAttempt) {
		t.Fatalf("expired: %v", err)
	}
}

func TestCompleteExchangesCodeAndVerifiesSignedIdentity(t *testing.T) {
	now := time.Now()
	key := testKey(t)
	var attempt Attempt
	var login url.Values
	var requestedToken bool

	c := fakeClient(func() time.Time { return now }, func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case tokenURL:
			if requestedToken || r.Method != http.MethodPost || r.Header.Get("Authorization") != "" {
				t.Fatal("incorrect token exchange")
			}
			requestedToken = true
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("client_id") != idFixture || r.Form.Get("code") != "code_fixture" ||
				r.Form.Get("redirect_uri") != redirectFixture || r.Form.Get("code_verifier") != attempt.pending.verifier ||
				r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("resource") != resource {
				t.Fatal("token exchange parameters do not match the attempt")
			}
			claims := makeClaims(now, login.Get("nonce"), idFixture)
			return jsonResponse(t, map[string]any{
				"access_token": "fixture_access", "refresh_token": "fixture_refresh", "token_type": "Bearer",
				"scope": scopes, "expires_in": 3600, "id_token": signToken(t, key, claims),
			}), nil
		case jwksURL:
			if !requestedToken || r.Method != http.MethodGet || r.Header.Get("Authorization") != "" {
				t.Fatal("incorrect signing-key request")
			}
			return jsonResponse(t, keysFor(key)), nil
		default:
			t.Fatal("unexpected network destination")
			return nil, nil
		}
	})
	attempt, login = makeAttempt(t, c)
	query := url.Values{"state": {login.Get("state")}, "code": {"code_fixture"}, "client_id": {idFixture}}
	account, err := c.Complete(context.Background(), attempt, query)
	if err != nil || account.Profile() != (Profile{Name: "Test", Email: "test@example.com", CanUsePlan: true, subject: "fixture-user"}) {
		t.Fatalf("validated account: %v", err)
	}
	visible, err := json.Marshal(account)
	if err != nil || string(visible) != "{}" {
		t.Fatal("session fields are exposed in JSON")
	}
	if _, err := c.Complete(context.Background(), attempt, query); !errors.Is(err, ErrExpiredAttempt) {
		t.Fatalf("code was reusable: %v", err)
	}
}

func TestIdentityRejectsInvalidClaimsAndSignatures(t *testing.T) {
	key := testKey(t)
	keys := keysFor(key)
	now := time.Now()
	claims := makeClaims(now, "nonce_fixture", idFixture)
	token := signToken(t, key, claims)
	if _, err := verifyIdentity(token, idFixture, "nonce_fixture", keys, now); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	for _, broken := range []struct {
		name  string
		value any
	}{
		{"iss", "https://example.invalid"},
		{"aud", "oaiapp_other"},
		{"aud", []string{idFixture, "oaiapp_other"}},
		{"sub", ""},
		{"nonce", "wrong-nonce"},
		{"exp", now.Unix()},
		{"nbf", now.Add(time.Hour).Unix()},
	} {
		wrong := makeClaims(now, "nonce_fixture", idFixture)
		wrong[broken.name] = broken.value
		if _, err := verifyIdentity(signToken(t, key, wrong), idFixture, "nonce_fixture", keys, now); !errors.Is(err, ErrIdentity) {
			t.Fatalf("wrong %s: %v", broken.name, err)
		}
	}
	parts := strings.Split(token, ".")
	binary, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	binary[0] ^= 1
	parts[2] = base64.RawURLEncoding.EncodeToString(binary)
	if _, err := verifyIdentity(strings.Join(parts, "."), idFixture, "nonce_fixture", keys, now); !errors.Is(err, ErrIdentity) {
		t.Fatalf("tampered signature accepted: %v", err)
	}
}

func TestModelsRespondAndSignOutUseOnlySession(t *testing.T) {
	now := time.Now()
	account := makeSession(now, scopes)
	ctx := context.Background()
	var inference, revocation bool
	c := fakeClient(func() time.Time { return now }, func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case modelsURL:
			if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer fixture_access" {
				t.Fatal("model request didn't use session")
			}
			return catalogResponse(), nil
		case responsesURL:
			inference = true
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer fixture_access" ||
				r.Header.Get("Accept") != "text/event-stream" {
				t.Fatal("inference request didn't use session")
			}
			var payload map[string]json.RawMessage
			if json.NewDecoder(r.Body).Decode(&payload) != nil || len(payload) != 4 ||
				string(payload["store"]) != "false" || string(payload["stream"]) != "true" ||
				string(payload["model"]) != "\"fixture-model\"" ||
				string(payload["input"]) != "[{\"role\":\"user\",\"content\":\"Brief demo\"}]" {
				t.Fatal("invalid plan-compatible Responses payload")
			}
			body := deltaFixture + "\r\n\r\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"NQ/ES\"}\r\n\r\n" +
				finalFixture + "\r\n\r\n"
			return fixtureResponse(200, "text/event-stream; charset=utf-8", body), nil
		case discoveryURL:
			return jsonResponse(t, map[string]string{"issuer": issuer, "revocation_endpoint": revocationURL}), nil
		case revocationURL:
			revocation = true
			if r.ParseForm() != nil || r.Form.Get("token") != "fixture_refresh" ||
				r.Form.Get("token_type_hint") != "refresh_token" || r.Form.Get("client_id") != idFixture {
				t.Fatal("invalid revocation request")
			}
			return fixtureResponse(200, "application/json", ""), nil
		default:
			t.Fatal("unexpected network destination")
			return nil, nil
		}
	})
	catalog, err := c.Models(ctx, account)
	if err != nil || len(catalog) != 1 || catalog[0] != (Model{ID: "fixture-model", DisplayName: "Test model"}) {
		t.Fatalf("unexpected catalog: %#v %v", catalog, err)
	}
	if _, err := c.Respond(ctx, account, "hidden", "Brief demo"); !errors.Is(err, ErrModel) || inference {
		t.Fatal("unlisted model reached inference")
	}
	got, err := c.Respond(ctx, account, "fixture-model", "Brief demo")
	if err != nil || got != "Séance NQ/ES" || !inference {
		t.Fatalf("response = %q, %v", got, err)
	}
	if err := c.SignOut(ctx, account); err != nil || !revocation || account.Profile() != (Profile{}) ||
		account.tokens.access != "" || account.tokens.refresh != "" {
		t.Fatalf("signout did not clear the local session: %v", err)
	}
	if _, err := c.Models(ctx, account); !errors.Is(err, ErrSignedOut) {
		t.Fatalf("signed-out catalog: %v", err)
	}
}

func TestConcurrentCallsRotateRefreshOnce(t *testing.T) {
	now := time.Now()
	account := makeSession(now, scopes)
	account.tokens.expiry = now
	var rotations atomic.Int32
	c := fakeClient(func() time.Time { return now }, func(r *http.Request) (*http.Response, error) {
		if r.URL.String() == tokenURL {
			if rotations.Add(1) != 1 || r.ParseForm() != nil || r.Form.Get("grant_type") != "refresh_token" ||
				r.Form.Get("refresh_token") != "fixture_refresh" || r.Form.Has("scope") {
				t.Error("incorrect or repeated token rotation")
			}
			return fixtureResponse(200, "application/json",
				`{"access_token":"new_access","refresh_token":"new_refresh","token_type":"Bearer","expires_in":3600}`), nil
		}
		if r.URL.String() != modelsURL || r.Header.Get("Authorization") != "Bearer new_access" {
			t.Error("request did not use rotated token")
		}
		return catalogResponse(), nil
	})
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Models(context.Background(), account); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if rotations.Load() != 1 || account.tokens.refresh != "new_refresh" {
		t.Fatal("expected exactly one refresh with latest token retained")
	}
}

func TestDeniedPlanAndTerminalRefreshFailSafely(t *testing.T) {
	now := time.Now()
	c := fakeClient(func() time.Time { return now }, func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != tokenURL {
			t.Fatal("API called without an authorized plan")
		}
		return fixtureResponse(400, "application/json", `{"error":"invalid_refresh_token","error_description":"sensitive fixture"}`), nil
	})
	identityOnly := makeSession(now, "openid profile")
	if _, err := c.Models(context.Background(), identityOnly); !errors.Is(err, ErrPlanNotGranted) {
		t.Fatalf("identity-only model access: %v", err)
	}
	if _, err := c.Respond(context.Background(), identityOnly, "fixture-model", "demo"); !errors.Is(err, ErrPlanNotGranted) {
		t.Fatalf("identity-only inference: %v", err)
	}
	expired := makeSession(now, scopes)
	expired.tokens.expiry = now
	if _, err := c.Models(context.Background(), expired); !errors.Is(err, ErrSessionExpired) ||
		expired.tokens.access != "" || expired.tokens.refresh != "" {
		t.Fatalf("terminal refresh was not cleared: %v", err)
	}
}

func TestStreamRequiresCompletedNonPartialText(t *testing.T) {
	for _, c := range []struct {
		body, want string
		err        error
	}{
		{deltaFixture + "\n\n" + finalFixture + "\n\n", "Séance", nil},
		{deltaFixture + "\r\r" + finalFixture + "\r\r", "Séance", nil},
		{deltaFixture + "\n\n" + finalFixture + "\n", "", ErrIncomplete},
		{deltaFixture + "\n\ndata: [DONE]\n\n", "", ErrIncomplete},
		{deltaFixture + "\n\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"subscription_sharing_usage_limit_exceeded\"}}}\n\n", "", ErrPlanLimit},
		{finalFixture + "\n\n", "", ErrIncomplete},
	} {
		got, err := readStream(strings.NewReader(c.body))
		if got != c.want || !errors.Is(err, c.err) {
			t.Fatalf("stream got %q, %v; wanted %q, %v", got, err, c.want, c.err)
		}
	}
}

func TestRespondHandlesUnexpectedResponseContentType(t *testing.T) {
	const completed = `{"object":"response","status":"completed","error":null,"incomplete_details":null,"output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Séance"}]}]}`
	const stream = deltaFixture + "\n\n" + finalFixture + "\n\n"
	for _, tc := range []struct {
		name, media, body, want, detail string
		wantErr                         error
	}{
		{name: "completed JSON", media: "application/json; charset=utf-8", body: completed, want: "Séance"},
		{name: "SSE without MIME", body: stream, want: "Séance"},
		{name: "SSE labeled plain text", media: "text/plain", body: "event: response.output_text.delta\n" + stream, want: "Séance"},
		{name: "unknown JSON", media: "application/json", body: `{"status":"completed","output":"internal payload"}`, wantErr: ErrIncomplete, detail: "objet Responses"},
		{name: "incomplete JSON", media: "application/json", body: strings.Replace(completed, `"status":"completed"`, `"status":"incomplete"`, 1), wantErr: ErrIncomplete, detail: "fin"},
		{name: "failed JSON", media: "application/json", body: strings.Replace(completed, `"error":null`, `"error":{"code":"internal payload"}`, 1), wantErr: ErrIncomplete, detail: "fin"},
		{name: "unconfirmed message", media: "application/json", body: strings.Replace(completed, `"role":"assistant","status":"completed"`, `"role":"assistant"`, 1), wantErr: ErrIncomplete, detail: "incomplet"},
		{name: "partial mislabeled stream", media: "text/plain", body: deltaFixture + "\n\n", wantErr: ErrIncomplete, detail: "confirmation de fin"},
		{name: "trailing markup", media: "application/json", body: completed + "<p>internal payload</p>", wantErr: ErrIncomplete, detail: "JSON"},
		{name: "HTML gateway", media: "text/html", body: "<p>internal payload</p>", wantErr: ErrIncomplete, detail: "HTML"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			account := makeSession(now, scopes)
			account.models = map[string]bool{"fixture-model": true}
			c := fakeClient(func() time.Time { return now }, func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != responsesURL {
					t.Fatalf("unexpected inference endpoint")
				}
				return fixtureResponse(200, tc.media, tc.body), nil
			})
			got, err := c.Respond(context.Background(), account, "fixture-model", "Brief demo")
			if got != tc.want || !errors.Is(err, tc.wantErr) ||
				(tc.detail != "" && (!strings.Contains(err.Error(), tc.detail) || strings.Contains(err.Error(), "internal payload"))) {
				t.Fatalf("got %q, %v; wanted %q, %v containing %q", got, err, tc.want, tc.wantErr, tc.detail)
			}
		})
	}
}

func TestStreamUsesConfirmedAssistantText(t *testing.T) {
	const unrelated = `data: {"type":"response.shell_call_output_content.delta","delta":{"stdout":"internal payload"}}`
	const finalText = `data: {"type":"response.output_text.done","text":"Texte final"}`
	const terminalOutput = `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"reasoning","content":[{"type":"reasoning_text","text":"internal payload"}]},{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Texte confirmé"}]}]}}`
	for _, tc := range []struct {
		frames []string
		want   string
	}{
		{[]string{unrelated, terminalOutput}, "Texte confirmé"},
		{[]string{deltaFixture, finalText, finalFixture}, "Texte final"},
		{[]string{deltaFixture, finalText, terminalOutput}, "Texte confirmé"},
	} {
		got, err := readStream(strings.NewReader(strings.Join(tc.frames, "\n\n") + "\n\n"))
		if err != nil || got != tc.want {
			t.Fatalf("stream got %q, %v; wanted %q", got, err, tc.want)
		}
	}
}

func TestStreamDoesNotReturnRefusalsOrPartialOutput(t *testing.T) {
	for _, tc := range []struct{ event, reason string }{
		{`{"type":"response.completed","response":{"status":"completed","output":null}}`, "illisible"},
		{`{"type":"response.completed","response":{"status":"completed","output":[]}}`, "aucun texte"},
		{`{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"refusal","refusal":"internal payload"}]}]}}`, "refusé"},
		{`{"type":"response.completed","response":{"status":"incomplete"}}`, "confirmé"},
		{`{"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}}}`, "limite de sortie"},
		{`{"type":"response.failed","response":{"error":{"code":"internal payload","message":"internal payload"}}}`, "échec de la génération"},
		{`{"type":"response.output_text.delta","delta":{"text":"internal payload"}}`, "illisible"},
	} {
		body := deltaFixture + "\n\ndata: " + tc.event + "\n\n"
		got, err := readStream(strings.NewReader(body))
		if got != "" || !errors.Is(err, ErrIncomplete) || !strings.Contains(err.Error(), tc.reason) || strings.Contains(err.Error(), "internal payload") {
			t.Fatalf("stream result %q, %v; wanted safe failure containing %q", got, err, tc.reason)
		}
	}
}

func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func keysFor(key *rsa.PrivateKey) signingKeys {
	return signingKeys{Keys: []signingKey{{Type: "RSA", ID: "fixture", Use: "sig", Algorithm: "RS256",
		Modulus: base64.RawURLEncoding.EncodeToString(key.N.Bytes()), Exponent: "AQAB"}}}
}

func makeClaims(now time.Time, nonce, audience string) map[string]any {
	return map[string]any{"iss": issuer, "sub": "fixture-user", "nonce": nonce, "aud": audience,
		"exp": now.Add(time.Hour).Unix(), "name": "Test", "email": "test@example.com", "email_verified": true}
}

func signToken(t *testing.T, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"fixture"}`))
	encoded, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	signingInput := header + "." + base64.RawURLEncoding.EncodeToString(encoded)
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func makeSession(now time.Time, scope string) *Session {
	lifetime, cancel := context.WithCancel(context.Background())
	return &Session{lifetime: lifetime, cancel: cancel, clientID: idFixture,
		profile: Profile{Name: "Test", CanUsePlan: hasScope(scope, planScope)},
		tokens:  tokens{access: "fixture_access", refresh: "fixture_refresh", expiry: now.Add(time.Hour), scope: scope}}
}

func catalogResponse() *http.Response {
	return fixtureResponse(200, "application/json", `{"models":[
		{"slug":"fixture-model","display_name":"Test model","visibility":"list"},
		{"slug":"hidden","visibility":"hidden"}, {"slug":"fixture-model","visibility":"list"}]}`)
}
