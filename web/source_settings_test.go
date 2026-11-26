package main

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"briefstudio/web/internal/sources"
)

func TestSourceSetupNeverReturnsTheCredential(t *testing.T) {
	s := testServer(t)
	directory := t.TempDir()
	s.sourceSettings = openSourceStore(directory)
	cookie := &http.Cookie{Name: s.cookieName, Value: s.browserKey}
	const token = "fixture-secret-x"
	const endpoint = "/api/sources/x"
	if w := request(s, "POST", endpoint, `{"target":"@deitaone","token":"`+token+`"}`, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("source setting with missing authorization: %d", w.Code)
	}
	w := request(s, "POST", endpoint, `{"target":"@deitaone","token":"`+token+`"}`, cookie)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"configured":true`) || strings.Contains(w.Body.String(), token) {
		t.Fatalf("source setup status %d, unsafe or missing metadata", w.Code)
	}
	if saved := openSourceStore(directory).settings(sources.X); saved.Target != "deitaone" || saved.Token != "" {
		t.Fatal("memory-only preference did not retain only the account")
	}
	latest := time.Date(2026, 11, 26, 10, 00, 0, 0, time.UTC)
	s.fetchSource = func(_ context.Context, provider sources.Provider, settings sources.Settings, _ time.Time) ([]sources.Headline, error) {
		if provider != sources.X || settings.Target != "deitaone" || settings.Token != token {
			t.Fatal("the server did not get the saved profile")
		}
		return []sources.Headline{{Text: "fixture text", PublishedAt: latest}}, nil
	}
	w = request(s, "POST", endpoint+"/test", `{}`, cookie)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"count":1`) || !strings.Contains(w.Body.String(), latest.Format(time.RFC3339)) || strings.Contains(w.Body.String(), "fixture text") {
		t.Fatal("test endpoint failed or exposed a post")
	}
	w = request(s, "POST", endpoint, `{"target":"deitaone","persistent":true}`, cookie)
	if w.Code != http.StatusOK || openSourceStore(directory).settings(sources.X).Token != token || strings.Contains(w.Body.String(), token) {
		t.Fatal("explicit persistence did not restore the credential privately")
	}
	w = request(s, "POST", endpoint, `{"target":"deitaone","persistent":false}`, cookie)
	saved, err := os.ReadFile(s.sourceSettings.path(sources.X))
	if w.Code != http.StatusOK || err != nil || strings.Contains(string(saved), token) || s.sourceSettings.settings(sources.X).Token != token {
		t.Fatal("turning off persistence should immediately remove the saved credential")
	}
	w = request(s, "POST", endpoint, `{"remove":true}`, cookie)
	if w.Code != http.StatusOK || s.sourceSettings.settings(sources.X).Token != "" {
		t.Fatal("disconnection should clear the current credential")
	}
	if w := request(s, "POST", endpoint+"/test", `{}`, cookie); w.Code != http.StatusConflict {
		t.Fatalf("test accepted a disconnected source: %d", w.Code)
	}
}
