package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"briefstudio/web/internal/sources"
)

func TestGenerateUsesOnlyTheSelectedMarketDataPerContract(t *testing.T) {
	now := dateAt("2026-11-24T14:00:00Z")
	flip, vwap, poc := 20175.0, 20010.25, 6030.0
	snapshot := sources.MarketSnapshot{
		Schema: sources.MarketSnapshotSchema, ObservedAt: now.Add(-time.Hour),
		Source: "Export de test", SourceURL: "https://example.org/market",
		Instruments: map[string]sources.MarketInstrument{
			"NQ": {Symbol: "NQ=F", Gamma: &sources.GammaInput{Regime: "positive", Flip: &flip}, Levels: &sources.MarketLevels{VWAP: &vwap}},
			"ES": {Symbol: "ES=F", Levels: &sources.MarketLevels{POC: &poc}},
		},
	}
	history := barsAt("2026-11-23T14:30:00Z", 78)
	load := func(_ context.Context, endpoint, _ string, _ int64) ([]byte, error) {
		switch {
		case strings.Contains(endpoint, "NQ%3DF"):
			return seriesFixture(nq, now, 600, 20000, history), nil
		case strings.Contains(endpoint, "ES%3DF"):
			return seriesFixture(es, now, 600, 6000, history), nil
		default:
			return nil, errors.New("calendar fixture unavailable")
		}
	}
	for _, mode := range []GEXMode{"", WithoutGEX, WithGEX} {
		t.Run(string(mode), func(t *testing.T) {
			doc, err := generateWithSources(context.Background(), "", "", func(_ context.Context, prompt string) (string, error) {
				if !strings.Contains(prompt, `"ES":{"symbol":"ES=F","levels":{"poc":6030}}`) ||
					!strings.Contains(prompt, `"levels":{"vwap":20010.25}`) || strings.Contains(prompt, snapshot.SourceURL) {
					t.Fatal("imported context is missing, crossed between contracts, or includes the citation URL")
				}
				gamma := strings.Contains(prompt, `"gamma":{"regime":"positive","flip":20175}`)
				if gamma != (mode == WithGEX) {
					t.Fatal("the gamma payload does not match the requested mode")
				}
				return `{"nq":{"view":"hausse","watch":"gex"},"es":{"view":"attente","watch":"niveaux"}}`, nil
			}, load, func() time.Time { return now }, GenerationSources{Market: &snapshot, GEXMode: mode})
			if err != nil {
				t.Fatal(err)
			}
			_, markets, found := strings.Cut(doc.Text, "## NQ")
			nqText, esText, foundES := strings.Cut(markets, "## ES")
			if !found || !foundES || !strings.Contains(nqText, "VWAP 20\u202f010,25") ||
				!strings.Contains(esText, "POC 6\u202f030,00") || strings.Contains(esText, "régime positif") ||
				!strings.Contains(nqText, "Attente dans la fourchette RTH") {
				t.Fatal("imported values changed market scope or bypassed the RTH decision")
			}
			if mode == WithGEX {
				if !strings.Contains(doc.Text, "Avec GEX importé") ||
					!strings.Contains(nqText, "régime positif · bascule gamma 20\u202f175,00") ||
					!strings.Contains(nqText, "autour des repères gamma importés") ||
					!strings.Contains(esText, "Indisponible pour ce contrat") {
					t.Fatal("incomplete attribution or coverage in GEX mode")
				}
			} else if !strings.Contains(doc.Text, "Sans GEX") ||
				strings.Contains(nqText, "repères gamma") || strings.Contains(nqText, "régime positif") {
				t.Fatal("the default or without mode rendered gamma")
			}
			if doc.Sources[len(doc.Sources)-1].URL != snapshot.SourceURL {
				t.Fatal("the imported citation is missing from the document")
			}
		})
	}
	// Filtering one run must not mutate the input used by another.
	if snapshot.Instruments["NQ"].Gamma == nil {
		t.Fatal("the input document was mutated")
	}
}

func TestUnavailableGammaStopsBeforeProviderCalls(t *testing.T) {
	now := dateAt("2026-11-24T14:00:00Z")
	poc := 6040.0
	snapshot := sources.MarketSnapshot{
		Schema: sources.MarketSnapshotSchema, ObservedAt: now.Add(-time.Hour), Source: "Profil de test",
		Instruments: map[string]sources.MarketInstrument{"ES": {Symbol: "ES=F", Levels: &sources.MarketLevels{POC: &poc}}},
	}
	load := func(_ context.Context, _, _ string, _ int64) ([]byte, error) {
		t.Error("provider called after an invalid GEX request")
		return nil, nil
	}
	analyze := func(_ context.Context, _ string) (string, error) {
		t.Error("model called after an invalid GEX request")
		return "", nil
	}
	stale := snapshot
	stale.ObservedAt = now.Add(-25 * time.Hour)
	stale.Instruments = map[string]sources.MarketInstrument{"NQ": {Symbol: "NQ=F", Gamma: &sources.GammaInput{Regime: "negative"}}}
	for _, input := range []*sources.MarketSnapshot{nil, &snapshot, &stale} {
		_, err := generateWithSources(context.Background(), "", "", analyze, load, func() time.Time { return now },
			GenerationSources{GEXMode: WithGEX, Market: input})
		if !errors.Is(err, ErrGEXUnavailable) {
			t.Fatalf("expected a clear missing GEX error, got %v", err)
		}
	}
	context, err := selectMarketInput(WithoutGEX, &stale, now)
	if err != nil || !context.stale || len(context.instruments) != 0 || strings.Contains(context.prompt(), "negative") {
		t.Fatal("the without mode leaked expired values")
	}
}

func TestPublicGammaIsLocalAndRetainsImportedProfileLevels(t *testing.T) {
	now := dateAt("2026-11-24T14:00:00Z")
	wall, vwap := 20477.25, 20015.50
	public := &sources.MarketSnapshot{
		Schema: sources.MarketSnapshotSchema, ObservedAt: now.Add(-2 * time.Hour),
		Source: "FlashAlpha · exemple de test", SourceURL: "https://flashalpha.com/methodology/futures",
		Instruments: map[string]sources.MarketInstrument{
			"NQ": {Symbol: "NQ=F", Gamma: &sources.GammaInput{CallWall: &wall}},
		},
	}
	local := &sources.MarketSnapshot{
		Schema: sources.MarketSnapshotSchema, ObservedAt: now.Add(-time.Hour), Source: "Profil de test",
		Instruments: map[string]sources.MarketInstrument{
			"NQ": {Symbol: "NQ=F", Levels: &sources.MarketLevels{VWAP: &vwap}, Gamma: &sources.GammaInput{Regime: "neutral"}},
		},
	}
	load := func(_ context.Context, endpoint, _ string, _ int64) ([]byte, error) {
		switch {
		case strings.Contains(endpoint, "NQ%3DF"):
			return seriesFixture(nq, now, 600, 20000, nil), nil
		case strings.Contains(endpoint, "ES%3DF"):
			return seriesFixture(es, now, 600, 6000, nil), nil
		default:
			return nil, errors.New("not available in fixture")
		}
	}
	doc, err := generateWithSources(context.Background(), "", "", func(_ context.Context, prompt string) (string, error) {
		if strings.Contains(prompt, "20477.25") || strings.Contains(prompt, "FlashAlpha") ||
			strings.Contains(prompt, `"gamma":`) || !strings.Contains(prompt, `"vwap":20015.5`) {
			t.Fatal("public data or manually imported gamma leaked into the public mode prompt")
		}
		return `{"nq":{"view":"attente","watch":"gex"},"es":{"view":"attente","watch":"confirmation"}}`, nil
	}, load, func() time.Time { return now }, GenerationSources{Market: local, PublicGEX: public, GEXMode: PublicGEX})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc.Text, "Sans GEX · panneau public facultatif") || !strings.Contains(doc.Text, "VWAP 20\u202f015,50") ||
		strings.Contains(doc.Text, "20\u202f477,25") || strings.Contains(doc.Text, "repères gamma importés") ||
		!strings.Contains(doc.LocalGEX, "**NQ** call wall 20\u202f477,25") ||
		!strings.Contains(doc.LocalGEX, "**ES** Aucun relevé récent.") {
		t.Fatal("public values are missing locally, crossed between markets or shared with the brief body")
	}
	public.ObservedAt = now.Add(-25 * time.Hour)
	if _, err := publicGEXReadout(public, now); !errors.Is(err, sources.ErrPublicGEXUnavailable) {
		t.Fatal("stale public gamma should be unavailable")
	}
}
