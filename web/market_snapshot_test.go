package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"briefstudio/web/internal/engine"
	"briefstudio/web/internal/sources"
)

func TestMarketSourceValidatesAndPreservesTheLastImport(t *testing.T) {
	s := testServer(t)
	dir := t.TempDir()
	s.sourceSettings = openSourceStore(dir)
	cookie := &http.Cookie{Name: s.cookieName, Value: s.browserKey}
	const route = "/api/sources/market"

	if w := request(s, "GET", route+"/template", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatal("market template served without the local session")
	}
	demo := request(s, "GET", route+"/template", "", cookie)
	var data sources.MarketSnapshot
	now := time.Now()
	if demo.Code != http.StatusOK || !strings.Contains(demo.Header().Get("Content-Disposition"), "attachment") ||
		json.Unmarshal(demo.Body.Bytes(), &data) != nil || data.Validate(now) != nil || data.Fresh(now) {
		t.Fatal("template should have a valid structure and be stale by design")
	}
	data.Source = "Outil de test"
	data.ObservedAt = now.Add(-time.Hour)
	body := func(snapshot sources.MarketSnapshot) string {
		bytes, err := json.Marshal(struct {
			Action   string                 `json:"action"`
			Snapshot sources.MarketSnapshot `json:"snapshot"`
		}{"import", snapshot})
		if err != nil {
			t.Fatal(err)
		}
		return string(bytes)
	}
	if w := request(s, "POST", route, body(data), nil); w.Code != http.StatusUnauthorized {
		t.Fatal("market import bypassed the local session")
	}
	if w := request(s, "POST", route, body(data), cookie); w.Code != http.StatusOK {
		t.Fatalf("market import returned %d: %s", w.Code, w.Body)
	}
	bad := data
	bad.Instruments = map[string]sources.MarketInstrument{"ES": {Symbol: "SPX", Gamma: &sources.GammaInput{Regime: "positive"}}}
	if w := request(s, "POST", route, body(bad), cookie); w.Code != http.StatusBadRequest {
		t.Fatal("cash levels accepted as futures levels")
	}
	s.sourceSettings = openSourceStore(dir)
	status := request(s, "GET", route, "", cookie)
	var state marketSourceState
	if status.Code != http.StatusOK || json.Unmarshal(status.Body.Bytes(), &state) != nil || state.Snapshot == nil ||
		state.Snapshot.Source != data.Source || !state.Snapshot.Fresh || strings.Join(state.Snapshot.Gamma, ",") != "NQ,ES" ||
		strings.Join(state.Snapshot.Levels, ",") != "NQ,ES" {
		t.Fatal("valid import did not survive replacement failure and restart")
	}
	renewed, err := s.sourceSettings.readMarketSnapshot(now.Add(25 * time.Hour))
	if err != nil || renewed == nil || renewed.Fresh(now.Add(25*time.Hour)) {
		t.Fatal("reopened snapshot never becomes stale")
	}
	if w := request(s, "POST", route, `{"action":"remove"}`, cookie); w.Code != http.StatusOK ||
		strings.TrimSpace(w.Body.String()) != `{"snapshot":null}` {
		t.Fatal("remove did not clear the market snapshot")
	}
	if w := request(s, "POST", "/api/generate", `{"model":"test","gexMode":"auto"}`, cookie); w.Code != http.StatusBadRequest {
		t.Fatal("unsupported mode was accepted")
	}
}

func TestPublicGEXReadAndExportBoundaries(t *testing.T) {
	s := testServer(t)
	s.sourceSettings = openSourceStore(t.TempDir())
	cookie := &http.Cookie{Name: s.cookieName, Value: s.browserKey}
	calls := 0
	s.fetchPublicGEX = func(_ context.Context, cutoff time.Time) (*sources.MarketSnapshot, error) {
		calls++
		if calls == 2 {
			return nil, sources.ErrPublicGEXUnavailable
		}
		return &sources.MarketSnapshot{
			Schema: sources.MarketSnapshotSchema, ObservedAt: cutoff.Add(-time.Hour), Source: "Source de test",
			Instruments: map[string]sources.MarketInstrument{
				"NQ": {Symbol: "NQ=F", Gamma: &sources.GammaInput{Regime: "negative"}},
			},
		}, nil
	}
	const route = "/api/sources/market/public/test"
	if w := request(s, "POST", route, `{}`, nil); w.Code != http.StatusUnauthorized || calls != 0 {
		t.Fatal("public data test should require the local browser session")
	}
	if w := request(s, "POST", route, `{"url":"https://example.com"}`, cookie); w.Code != http.StatusBadRequest || calls != 0 {
		t.Fatal("public data test should not accept a target URL")
	}
	if w := request(s, "GET", "/api/sources/market", "", cookie); w.Code != http.StatusOK || calls != 0 {
		t.Fatal("opening source status should not request public data")
	}
	if w := request(s, "POST", route, `{}`, cookie); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"gamma":["NQ"]`) || calls != 1 {
		t.Fatal("test did not report public coverage")
	}
	if w := request(s, "GET", "/api/sources/market", "", cookie); w.Code != http.StatusOK ||
		strings.TrimSpace(w.Body.String()) != `{"snapshot":null}` {
		t.Fatal("public GEX test replaced the user import")
	}
	if w := request(s, "POST", route, `{}`, cookie); w.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(w.Body.String(), "24 h") {
		t.Fatal("missing public data should show an actionable message")
	}

	doc := engine.Sample()
	doc.LocalGEX = "## GEX public différé\n\nfixture-vendor-number"
	id, err := s.library.save(doc)
	if err != nil {
		t.Fatal(err)
	}
	if w := request(s, "GET", "/api/briefs/"+id, "", cookie); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), "fixture-vendor-number") {
		t.Fatal("local brief lost its public GEX block")
	}
	w := request(s, "GET", "/api/briefs/"+id+"/export", "", cookie)
	marker := regexp.MustCompile(`(?s)id="embedded-brief"[^>]*>\s*([A-Za-z0-9+/=]+)\s*<`)
	match := marker.FindSubmatch(w.Body.Bytes())
	if w.Code != http.StatusOK || len(match) != 2 {
		t.Fatal("missing embedded brief")
	}
	body, err := base64.StdEncoding.DecodeString(string(match[1]))
	if err != nil || strings.Contains(string(body), "localGex") || strings.Contains(string(body), "fixture-vendor-number") {
		t.Fatal("shareable export leaked local public GEX")
	}
}
