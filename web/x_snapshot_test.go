package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"briefstudio/web/internal/sources"
)

func TestXSnapshotAvoidsProviderCallsAndSurvivesRestart(t *testing.T) {
	s := testServer(t)
	dir := t.TempDir()
	s.sourceSettings = openSourceStore(dir)
	cookie := &http.Cookie{Name: s.cookieName, Value: s.browserKey}
	if status, err := s.sourceSettings.change(sources.X, sourceChange{Target: "deitaone", Token: "fixture-api-token"}); status != http.StatusOK || err != nil {
		t.Fatal("fixture API configuration failed")
	}
	s.fetchSource = func(_ context.Context, provider sources.Provider, _ sources.Settings, _ time.Time) ([]sources.Headline, error) {
		t.Errorf("unexpected HTTP fetch for %s", provider)
		return nil, nil
	}
	now := time.Now().UTC()
	data := sources.XSnapshot{
		Schema: sources.XSnapshotSchema, ObservedAt: now,
		Posts: []sources.XSnapshotPost{
			{ID: "190001", Account: "Deitaone", PublishedAt: now.Add(-time.Hour)},
			{ID: "190001", Account: "deitaone", PublishedAt: now.Add(-time.Hour)},
			{ID: "190002", Account: "deitaone", PublishedAt: now.Add(-40 * time.Hour)},
		},
	}
	body, _ := json.Marshal(data)
	const route = "/api/sources/x/snapshot"
	if w := request(s, "POST", route, string(body), nil); w.Code != http.StatusUnauthorized {
		t.Fatal("file import was not authenticated")
	}
	if w := request(s, "POST", route, string(body), cookie); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"count":1`) {
		t.Fatalf("could not import the X file: %d", w.Code)
	}
	if w := request(s, "POST", "/api/sources/x/test", `{}`, cookie); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"count":1`) {
		t.Fatal("X test did not use the file instead of the API")
	}
	s.sourceSettings = openSourceStore(dir)
	feeds := s.gatherOptionalNews(context.Background(), time.Now())
	if len(feeds) != 1 || len(feeds[0].Items) != 1 || feeds[0].Items[0].URL != "https://x.com/deitaone/status/190001" {
		t.Fatalf("unexpected restored X headlines: %+v", feeds)
	}
	feeds = s.gatherOptionalNews(context.Background(), now.Add(37*time.Hour))
	if len(feeds) != 1 || feeds[0].Failure == "" || len(feeds[0].Items) != 0 {
		t.Fatal("X file should expire without switching to the API")
	}
	if w := request(s, "POST", route, `{"schema":"bad","posts":[]}`, cookie); w.Code != http.StatusBadRequest {
		t.Fatal("invalid JSON snapshot should be rejected")
	}
	if w := request(s, "POST", route, `{"remove":true}`, cookie); w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"snapshot"`) {
		t.Fatal("remove should clear the stored snapshot")
	}
}

func TestBirdRefreshStoresOnlyASuccessfulSnapshot(t *testing.T) {
	s := testServer(t)
	s.sourceSettings = openSourceStore(t.TempDir())
	cookie := &http.Cookie{Name: s.cookieName, Value: s.browserKey}
	const route = "/api/sources/x/bird"
	if w := request(s, "POST", route, `{}`, cookie); w.Code != http.StatusConflict {
		t.Fatalf("Bird without connector: %d", w.Code)
	}
	count := 0
	s.refreshBird = func(context.Context) (sources.XSnapshot, error) {
		count++
		if count > 1 {
			return sources.XSnapshot{}, context.DeadlineExceeded
		}
		now := time.Now().UTC()
		return sources.XSnapshot{Schema: sources.XSnapshotSchema, ObservedAt: now,
			Posts: []sources.XSnapshotPost{{ID: "12101", Account: "deitaone", PublishedAt: now.Add(-time.Minute)}}}, nil
	}
	if w := request(s, "POST", route, `{}`, nil); w.Code != http.StatusUnauthorized || count != 0 {
		t.Fatal("the local launcher must authorize Bird calls")
	}
	if w := request(s, "GET", "/api/sources", "", cookie); !strings.Contains(w.Body.String(), `"birdAvailable":true`) {
		t.Fatal("configured Bird was not announced")
	}
	if w := request(s, "POST", route, `{}`, cookie); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"count":1`) {
		t.Fatalf("could not refresh Bird references: %d", w.Code)
	}
	if w := request(s, "POST", route, `{}`, cookie); w.Code != http.StatusBadGateway {
		t.Fatalf("failed Bird refresh: %d", w.Code)
	}
	if status := s.sourceSettings.snapshotStatus(time.Now()); status == nil || status.Count != 1 {
		t.Fatal("failed refresh should preserve the last successful snapshot")
	}
}
