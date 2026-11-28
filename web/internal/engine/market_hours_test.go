package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"briefstudio/web/internal/sources"
)

func TestEquityFuturesClosureHours(t *testing.T) {
	for _, tc := range []struct {
		name string
		at   string
		want bool
	}{
		{"cash close is not futures close", "2026-11-24T21:15:00Z", false},
		{"maintenance begins", "2026-11-24T22:00:00Z", true},
		{"maintenance ends", "2026-11-24T23:00:00Z", false},
		{"Friday futures close", "2026-12-04T22:00:00Z", true},
		{"Saturday", "2026-11-28T16:00:00Z", true},
		{"Sunday before opening", "2026-11-29T22:59:59Z", true},
		{"Sunday Globex reopening", "2026-11-29T23:00:00Z", false},
		{"summer maintenance daylight saving", "2026-08-04T21:05:00Z", true},
		{"Thanksgiving before the halt", "2026-11-26T17:59:59Z", false},
		{"Thanksgiving early halt", "2026-11-26T18:00:00Z", true},
		{"Thanksgiving reopens", "2026-11-26T23:00:00Z", false},
		{"unknown special hours leave original gate", "2026-12-25T18:00:00Z", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := MarketClosed(dateAt(tc.at)); got != tc.want {
				t.Fatalf("MarketClosed(%s) = %v, want %v", tc.at, got, tc.want)
			}
		})
	}
}

func TestClosedMarketArchivesDatedFactsWithoutInferenceOrPersonalSources(t *testing.T) {
	cutoff := dateAt("2026-11-26T19:00:00Z")
	nqBars := barsAt("2026-11-25T14:30:00Z", 78)
	for _, malformed := range []struct {
		name string
		inst instrument
		age  int64
	}{
		{"wrong contract", nq, 3601},
		{"future quote", es, -1},
		{"implausibly old quote", es, 15 * 24 * 3600},
	} {
		t.Run(malformed.name, func(t *testing.T) {
			load := func(_ context.Context, endpoint, _ string, _ int64) ([]byte, error) {
				switch {
				case strings.Contains(endpoint, "NQ%3DF"):
					return seriesFixture(nq, cutoff, 3601, 20019.25, nqBars), nil
				case strings.Contains(endpoint, "ES%3DF"):
					return seriesFixture(malformed.inst, cutoff, malformed.age, 1800.75, nil), nil
				default:
					return nil, errors.New("not available in fixture")
				}
			}
			analysisCalled, personalCalled := false, false
			doc, err := generateWithSources(context.Background(), "", "", func(context.Context, string) (string, error) {
				analysisCalled = true
				return "", errors.New("analysis should be skipped")
			}, load, func() time.Time { return cutoff }, GenerationSources{
				News: func(context.Context, time.Time) []NewsFeed {
					personalCalled = true
					return nil
				},
			})
			if err != nil || analysisCalled || personalCalled {
				t.Fatalf("failed factual recap or called optional providers: analysis=%v personal=%v err=%v", analysisCalled, personalCalled, err)
			}
			if !doc.MarketClosed || !strings.Contains(doc.Title, "marché fermé") {
				t.Fatalf("missing market closure status: %+v", doc)
			}
			for _, expected := range []string{"20\u202f019,25 points au 2026-11-26 12:59 ET", "Séance RTH du 2026-11-25", "Calendrier économique US"} {
				if !strings.Contains(doc.Text, expected) {
					t.Errorf("missing %q in factual recap", expected)
				}
			}
			for _, unsupported := range []string{"1\u202f800,75", "**Vue**", "**À surveiller**"} {
				if strings.Contains(doc.Text, unsupported) {
					t.Errorf("unsupported observation or analysis %q reached recap", unsupported)
				}
			}
			if doc.Sources[0].URL != cmeScheduleURL {
				t.Fatalf("missing closure timetable source: %+v", doc.Sources)
			}
		})
	}
}

func TestReopeningAndUnverifiedHolidaysRequireFreshQuotesAndAnalysis(t *testing.T) {
	for _, at := range []string{"2026-11-26T23:00:00Z", "2026-12-25T18:00:00Z"} {
		cutoff := dateAt(at)
		clock := func() time.Time { return cutoff }
		load := func(_ context.Context, endpoint, _ string, _ int64) ([]byte, error) {
			switch {
			case strings.Contains(endpoint, "NQ%3DF"):
				return seriesFixture(nq, cutoff, 3601, 20100, nil), nil
			case strings.Contains(endpoint, "ES%3DF"):
				return seriesFixture(es, cutoff, 3601, 6100, nil), nil
			default:
				return nil, errors.New("not available in fixture")
			}
		}
		if _, err := generate(context.Background(), "", "", nil, load, clock); !errors.Is(err, ErrMarketOpen) {
			t.Errorf("nil analysis accepted at %s: %v", at, err)
		}
		_, err := generate(context.Background(), "", "", func(context.Context, string) (string, error) {
			t.Fatal("stale prices should not reach inference while closure is unconfirmed")
			return "", nil
		}, load, clock)
		if err == nil || !strings.Contains(err.Error(), "Aucune cotation récente") {
			t.Errorf("expected freshness gate at %s: %v", at, err)
		}
	}
}

func TestUnavailablePublicGEXIsOnlyALocalNotice(t *testing.T) {
	cutoff := dateAt("2026-11-24T14:00:00Z")
	price := 20209.25
	stale := &sources.MarketSnapshot{
		Schema: sources.MarketSnapshotSchema, Source: "Private fixture", ObservedAt: cutoff.Add(-25 * time.Hour),
		Instruments: map[string]sources.MarketInstrument{"NQ": {Symbol: "NQ=F", Gamma: &sources.GammaInput{CallWall: &price}}},
	}
	load := func(_ context.Context, endpoint, _ string, _ int64) ([]byte, error) {
		if strings.Contains(endpoint, "NQ%3DF") {
			return seriesFixture(nq, cutoff, 300, 20100, nil), nil
		}
		return nil, errors.New("not available in fixture")
	}
	for _, public := range []*sources.MarketSnapshot{nil, stale} {
		analysisCalled := false
		doc, err := generateWithSources(context.Background(), "", "", func(_ context.Context, prompt string) (string, error) {
			analysisCalled = true
			if strings.Contains(prompt, "20209.25") || strings.Contains(prompt, "Private fixture") {
				t.Fatal("stale public GEX reached the prompt")
			}
			return `{"nq":{"view":"attente","watch":"confirmation"},"es":{"view":"attente","watch":"confirmation"}}`, nil
		}, load, func() time.Time { return cutoff }, GenerationSources{PublicGEX: public, GEXMode: PublicGEX})
		if err != nil || !analysisCalled || doc.MarketClosed {
			t.Fatalf("public GEX outage blocked normal generation: %+v %v", doc, err)
		}
		if !strings.Contains(doc.LocalGEX, "Aucun relevé récent") ||
			!strings.Contains(doc.Text, "Sans GEX · panneau public facultatif") ||
			strings.Contains(doc.Text, "20\u202f209,25") || strings.Contains(doc.Text, "Aucun relevé récent") ||
			strings.Contains(doc.Text, "relevé GEX public est consultable") {
			t.Fatalf("missing local notice or public GEX status leaked to exported text: %+v", doc)
		}
	}
}
