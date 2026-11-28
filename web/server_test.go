package main

import (
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"briefstudio/web/internal/chatgpt"
	"briefstudio/web/internal/engine"
)

func testServer(t *testing.T) *studioServer {
	t.Helper()
	library, err := openLibrary(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	assets, err := fs.Sub(webAssets, "ui")
	if err != nil {
		t.Fatal(err)
	}
	return &studioServer{
		origin: "http://127.0.0.1:12345", host: "127.0.0.1:12345", cookieName: "brief_studio_12345",
		bootKey: "test-bootstrap", browserKey: "test-browser", library: library, client: chatgpt.New(), assets: assets,
	}
}

func request(s *studioServer, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, s.origin+path, strings.NewReader(body))
	r.Header.Set("Origin", s.origin)
	r.Header.Set("X-BriefStudio-Request", "1")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, r)
	return w
}

func TestLocalBootstrapAndOrigin(t *testing.T) {
	s := testServer(t)
	if w := request(s, "GET", "/api/state", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated library status = %d", w.Code)
	}
	if w := request(s, "POST", "/api/bootstrap", `{"key":"wrong"}`, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("invalid launcher key status = %d", w.Code)
	}
	w := request(s, "POST", "/api/bootstrap", `{"key":"test-bootstrap"}`, nil)
	cookies := w.Result().Cookies()
	if w.Code != http.StatusNoContent || len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("bootstrap status %d, expected one HttpOnly Lax session cookie", w.Code)
	}
	if w := request(s, "GET", "/api/state", "", cookies[0]); w.Code != http.StatusOK {
		t.Fatalf("authenticated app state status = %d", w.Code)
	}
	if w := request(s, "POST", "/api/generate", `{"model":"example"}`, cookies[0]); w.Code != http.StatusUnauthorized {
		t.Fatalf("generation without ChatGPT status = %d", w.Code)
	}
	if w := request(s, "POST", "/api/generate", `{"marketRecap":true}`, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("recap without a local browser session status = %d", w.Code)
	}
	if w := request(s, "POST", "/api/account/remember", `{}`, cookies[0]); w.Code != http.StatusUnauthorized {
		t.Fatalf("vault retry without an account status = %d", w.Code)
	}
	for _, tc := range []struct{ host, origin, fetchSite, header string }{
		{"example.invalid", s.origin, "same-origin", "1"},
		{s.host, "https://example.invalid", "cross-site", "1"},
		{s.host, "null", "same-origin", "1"},
		{s.host, s.origin, "same-origin", ""},
	} {
		for _, path := range []string{"/api/signout", "/api/account/remember", "/api/generate"} {
			r := httptest.NewRequest("POST", s.origin+path, strings.NewReader(`{"marketRecap":true}`))
			r.Host = tc.host
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Sec-Fetch-Site", tc.fetchSite)
			r.Header.Set("X-BriefStudio-Request", tc.header)
			r.AddCookie(cookies[0])
			w := httptest.NewRecorder()
			s.handler().ServeHTTP(w, r)
			if w.Code != http.StatusForbidden {
				t.Fatalf("unexpected cross-origin/host status %d at %s for %+v", w.Code, path, tc)
			}
		}
	}
}

func TestBrowserAuthRedirectAndSingleUseCallback(t *testing.T) {
	s := testServer(t)
	cookie := &http.Cookie{Name: s.cookieName, Value: s.browserKey}
	home := request(s, "GET", "/", "", nil)
	if home.Header().Get("Referrer-Policy") != "same-origin" || !strings.Contains(home.Header().Get("Content-Security-Policy"), "form-action 'self' https://auth.openai.com") {
		t.Fatal("browser policy must allow same-origin form followed by the OpenAI redirect")
	}
	start := request(s, "POST", "/auth/start", "", cookie)
	provider, err := url.Parse(start.Header().Get("Location"))
	if err != nil || start.Code != http.StatusSeeOther || provider.Host != "auth.openai.com" || provider.Scheme != "https" || provider.Query().Get("redirect_uri") != s.origin+"/auth/callback" {
		t.Fatal("expected a 303 to the official ChatGPT sign-in with the exact loopback callback")
	}
	state := provider.Query().Get("state")
	if state == "" {
		t.Fatal("missing OAuth state")
	}
	if miss := request(s, "GET", "/auth/callback?error=access_denied&state=wrong", "", cookie); miss.Code != http.StatusBadRequest {
		t.Fatalf("mismatched OAuth state status = %d", miss.Code)
	}
	path := "/auth/callback?error=access_denied&state=" + url.QueryEscape(state)
	if denied := request(s, "GET", path, "", cookie); denied.Code != http.StatusSeeOther || denied.Header().Get("Location") != "/?auth=error" {
		t.Fatal("cancelled authorization did not return to the app")
	}
	if reused := request(s, "GET", path, "", cookie); reused.Code != http.StatusBadRequest {
		t.Fatalf("replayed OAuth state status = %d", reused.Code)
	}
}

func TestArchiveAndOfflineExport(t *testing.T) {
	s := testServer(t)
	cookie := &http.Cookie{Name: s.cookieName, Value: s.browserKey}
	doc := engine.Sample()
	doc.Text += "\n</script><script>alert(1)</script>\n"
	doc.MarketClosed = true
	doc.LocalGEX = "Relevé FlashAlpha réservé au lecteur local."
	id, err := s.library.save(doc)
	if err != nil {
		t.Fatal(err)
	}
	listing := request(s, "GET", "/api/briefs", "", cookie)
	var entries []briefEntry
	if listing.Code != http.StatusOK || json.Unmarshal(listing.Body.Bytes(), &entries) != nil || len(entries) != 1 || entries[0].ID != id {
		t.Fatal("saved document missing from library")
	}
	w := request(s, "GET", "/api/briefs/"+id+"/export", "", cookie)
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") || strings.Contains(w.Body.String(), "</script><script>alert(1)</script>") {
		t.Fatal("export failed or inlined untrusted text")
	}
	matched := regexp.MustCompile(`>[\s]*([A-Za-z0-9+/=]{100,})[\s]*</script>`).FindStringSubmatch(w.Body.String())
	if len(matched) != 2 {
		t.Fatal("expected standalone HTML to embed the brief")
	}
	payload, err := base64.StdEncoding.DecodeString(matched[1])
	var exported engine.Document
	if err != nil || json.Unmarshal(payload, &exported) != nil || exported.Text != doc.Text ||
		!exported.MarketClosed || exported.LocalGEX != "" {
		t.Fatal("exported payload does not match saved brief")
	}
}
