package sources

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

func marketPrice(value float64) *float64 { return &value }

func marketCutoff() time.Time { return time.Date(2026, time.November, 26, 15, 0, 0, 0, time.UTC) }

func marketSnapshotFixture() MarketSnapshot {
	return MarketSnapshot{
		Schema: MarketSnapshotSchema, ObservedAt: marketCutoff().Add(-time.Hour), Source: "Étude locale (CME)",
		Instruments: map[string]MarketInstrument{
			"NQ": {Symbol: "NQ=F", Levels: &MarketLevels{POC: marketPrice(20000)}},
		},
	}
}

func TestMarketSnapshotWireSchemaAndPartialInputs(t *testing.T) {
	const raw = `{
		"schema":"brief-studio/market-input/v1",
		"observedAt":"2026-11-26T09:30:00-05:00",
		"source":"Étude locale (CME)",
		"sourceURL":"https://example.test/overview?symbol=NQ#method",
		"instruments":{
			"NQ":{"symbol":"NQ=F","gamma":{"regime":"neutral","flip":20100.5,"callWall":20000,"putWall":20200}},
			"ES":{"symbol":"ES=F","levels":{"poc":6000,"vah":6000,"val":5990,"vwap":6001.125}}
		}
	}`
	var snapshot MarketSnapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Validate(marketCutoff()); err != nil || !snapshot.Fresh(marketCutoff()) {
		t.Fatalf("valid wire input rejected: %v", err)
	}
	if snapshot.ObservedAt.UTC().Format(time.RFC3339) != "2026-11-26T14:30:00Z" ||
		*snapshot.Instruments["NQ"].Gamma.Flip != 20100.5 || *snapshot.Instruments["ES"].Levels.VWAP != 6001.125 {
		t.Fatalf("data changed on decode: %+v", snapshot)
	}
	snapshot.SourceURL = ""
	snapshot.Instruments = map[string]MarketInstrument{"ES": {Symbol: "ES=F", Gamma: &GammaInput{Regime: "positive"}}}
	if err := snapshot.Validate(marketCutoff()); err != nil {
		t.Fatalf("single-instrument gamma regime should work: %v", err)
	}
	if err := marketSnapshotFixture().Validate(marketCutoff()); err != nil {
		t.Fatalf("single level with no gamma should work: %v", err)
	}
}

func TestMarketSnapshotValidityAndFreshnessAreSeparate(t *testing.T) {
	cutoff := marketCutoff()
	snapshot := marketSnapshotFixture()
	snapshot.ObservedAt = cutoff.Add(-MaxMarketSnapshotAge)
	if err := snapshot.Validate(cutoff); err != nil || !snapshot.Fresh(cutoff) {
		t.Fatalf("the exact age boundary is eligible: %v", err)
	}
	snapshot.ObservedAt = snapshot.ObservedAt.Add(-time.Nanosecond)
	if err := snapshot.Validate(cutoff); err != nil || snapshot.Fresh(cutoff) {
		t.Fatalf("older value should remain valid but stale: %v", err)
	}
	for _, observedAt := range []time.Time{{}, cutoff.Add(time.Nanosecond)} {
		snapshot.ObservedAt = observedAt
		if snapshot.Validate(cutoff) == nil || snapshot.Fresh(cutoff) {
			t.Fatalf("missing or future observation accepted: %v", observedAt)
		}
	}
}

func TestMarketSnapshotRejectsInvalidMarketObservations(t *testing.T) {
	for _, test := range []struct {
		name       string
		key        string
		instrument MarketInstrument
	}{
		{"unapproved instrument", "NDX", MarketInstrument{Symbol: "NDX", Gamma: &GammaInput{Regime: "neutral"}}},
		{"crossed symbols", "ES", MarketInstrument{Symbol: "NQ=F", Gamma: &GammaInput{Regime: "neutral"}}},
		{"missing inputs", "NQ", MarketInstrument{Symbol: "NQ=F"}},
		{"empty gamma", "NQ", MarketInstrument{Symbol: "NQ=F", Gamma: &GammaInput{}}},
		{"empty levels", "NQ", MarketInstrument{Symbol: "NQ=F", Levels: &MarketLevels{}}},
		{"invalid regime", "NQ", MarketInstrument{Symbol: "NQ=F", Gamma: &GammaInput{Regime: "risk-on"}}},
		{"zero", "NQ", MarketInstrument{Symbol: "NQ=F", Levels: &MarketLevels{VWAP: marketPrice(0)}}},
		{"negative", "ES", MarketInstrument{Symbol: "ES=F", Gamma: &GammaInput{Flip: marketPrice(-2)}}},
		{"infinite", "NQ", MarketInstrument{Symbol: "NQ=F", Gamma: &GammaInput{PutWall: marketPrice(math.Inf(1))}}},
		{"nan", "ES", MarketInstrument{Symbol: "ES=F", Levels: &MarketLevels{POC: marketPrice(math.NaN())}}},
		{"outside bound", "NQ", MarketInstrument{Symbol: "NQ=F", Levels: &MarketLevels{VWAP: marketPrice(90_000_000)}}},
		{"partial area crossed", "ES", MarketInstrument{Symbol: "ES=F", Levels: &MarketLevels{VAL: marketPrice(6010), VAH: marketPrice(6000)}}},
		{"poc below area", "ES", MarketInstrument{Symbol: "ES=F", Levels: &MarketLevels{VAL: marketPrice(6010), POC: marketPrice(6000)}}},
		{"poc above area", "ES", MarketInstrument{Symbol: "ES=F", Levels: &MarketLevels{POC: marketPrice(6010), VAH: marketPrice(6000)}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := marketSnapshotFixture()
			snapshot.Instruments = map[string]MarketInstrument{test.key: test.instrument}
			if err := snapshot.Validate(marketCutoff()); err == nil {
				t.Fatal("unexpectedly accepted invalid market input")
			}
		})
	}
}

func TestMarketSnapshotRejectsUnsafeProvenance(t *testing.T) {
	for _, url := range []string{
		"http://example.test/chart", "javascript:alert(1)", "https://user:secret@example.test/",
		"https:///missing-host", "https://example.test/chart?API_KEY=private",
		"https://example.test/chart?X-Amz-Signature=private", "https://example.test/chart#access_token=private",
	} {
		snapshot := marketSnapshotFixture()
		snapshot.SourceURL = url
		if err := snapshot.Validate(marketCutoff()); err == nil || strings.Contains(err.Error(), "private") {
			t.Errorf("unsafe URL accepted or error echoed credentials: %v", err)
		}
	}
	for _, name := range []string{"", " ", "Dev\nBroker", "Dev\u2028Broker", "```Mock", strings.Repeat("é", 101)} {
		snapshot := marketSnapshotFixture()
		snapshot.Source = name
		if err := snapshot.Validate(marketCutoff()); err == nil {
			t.Errorf("unsafe name accepted: %q", name)
		}
	}
	snapshot := marketSnapshotFixture()
	snapshot.Schema = "brief-studio/market-input/v2"
	if err := snapshot.Validate(marketCutoff()); err == nil {
		t.Fatal("unknown evidence schema accepted")
	}
	snapshot = marketSnapshotFixture()
	snapshot.Instruments = nil
	if err := snapshot.Validate(marketCutoff()); err == nil {
		t.Fatal("empty instruments accepted")
	}
}
