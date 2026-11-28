package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCompletedCashWindows(t *testing.T) {
	wednesday := barsAt("2026-11-25T14:30:00Z", 78)
	thanksgiving := barsAt("2026-11-26T14:30:00Z", 78)
	wednesdayAndHoliday := append(append([]int64{}, wednesday...), thanksgiving...)
	now := dateAt("2026-11-27T15:00:00Z")
	obs := decodeObservation(seriesFixture(nq, now, 600, 20100, wednesdayAndHoliday), nq, now)
	if obs.session == nil || obs.session.day != "2026-11-25" || obs.session.low != 19950 || obs.session.high != 20050 {
		t.Fatalf("expected full Wednesday RTH, got %+v", obs.session)
	}
	missingOne := append(append([]int64{}, wednesday[:40]...), wednesday[41:]...)
	obs = decodeObservation(seriesFixture(nq, now, 600, 20100, missingOne), nq, now)
	if obs.session != nil || obs.quote == nil {
		t.Fatalf("partial RTH should be withheld independently of current quote: %+v", obs)
	}
	early := barsAt("2026-11-27T14:30:00Z", 42)
	now = dateAt("2026-11-27T19:00:00Z")
	obs = decodeObservation(seriesFixture(es, now, 2*3600, 6050, early), es, now)
	if obs.session == nil || obs.session.day != "2026-11-27" || obs.quote != nil {
		t.Fatalf("early-close RTH and stale quote should be independent: %+v", obs)
	}
}

func TestRejectsWrongSymbolAndFutureQuote(t *testing.T) {
	now := dateAt("2026-11-27T15:00:00Z")
	if obs := decodeObservation(seriesFixture(es, now, 600, 6050, nil), nq, now); obs.quote != nil || obs.session != nil {
		t.Fatalf("ES data cannot populate NQ: %+v", obs)
	}
	if obs := decodeObservation(seriesFixture(es, now, -150, 6050, nil), es, now); obs.quote != nil {
		t.Fatalf("future observation should be unavailable: %+v", obs)
	}
}

func TestGenerationUsesFactsAndSurvivesFedFailure(t *testing.T) {
	now := dateAt("2026-11-24T14:00:00Z")
	history := barsAt("2026-11-23T14:30:00Z", 78)
	load := func(_ context.Context, endpoint, _ string, _ int64) ([]byte, error) {
		switch {
		case strings.Contains(endpoint, "NQ%3DF"):
			return seriesFixture(nq, now, 600, 19800, history), nil
		case strings.Contains(endpoint, "ES%3DF"):
			return seriesFixture(es, now, 3600, 6000, history), nil
		default:
			return nil, errors.New("Fed offline")
		}
	}
	doc, err := generate(context.Background(), "Surveiller   le calendrier.\n", "https://example.org/source", func(_ context.Context, prompt string) (string, error) {
		for _, expected := range []string{"NQ\nCotation : 19800 points", "ES\nCotation : indisponible", `"Surveiller le calendrier."`, `tableau JSON : []`} {
			if !strings.Contains(prompt, expected) {
				return "", errors.New("expected prompt evidence: " + expected)
			}
		}
		if strings.Contains(prompt, "example.org") {
			return "", errors.New("source URL need not leave this process")
		}
		return "```json\n" + `{"nq":{"view":"baisse","watch":"bornes","target":33333},"es":{"view":"hausse","watch":"bornes"}}` + "\n```", nil
	}, load, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Hypothèse baissière sous la fourchette RTH", "Indisponible sans cotation récente", "Fil monétaire de la Fed indisponible", "2026-11-23 ET"} {
		if !strings.Contains(doc.Text, expected) {
			t.Errorf("expected %q in brief:\n%s", expected, doc.Text)
		}
	}
	if doc.Schema != schema || doc.GeneratedAt != "2026-11-24T14:00:00Z" || len(doc.Sources) != 3 || doc.Sources[2].URL != "https://example.org/source" {
		t.Fatalf("unexpected envelope: %s %s %+v", doc.Schema, doc.GeneratedAt, doc.Sources)
	}
	if strings.Contains(doc.Text, "33333") || strings.Contains(doc.Text, "Hypothèse haussière") {
		t.Fatal("unbacked model output appeared in brief")
	}
}

func TestNoInferenceWithoutMarketData(t *testing.T) {
	_, err := generate(context.Background(), "", "", func(_ context.Context, _ string) (string, error) {
		t.Fatal("AI called without fresh market data")
		return "", nil
	}, func(_ context.Context, _, _ string, _ int64) ([]byte, error) {
		return nil, errors.New("provider unavailable")
	}, func() time.Time { return dateAt("2026-11-24T14:00:00Z") })
	if err == nil || !strings.Contains(err.Error(), "Aucune cotation récente") {
		t.Fatalf("expected actionable missing quotes error, got %v", err)
	}
}

func TestRejectsIncompleteAssessmentAndUnsafeSource(t *testing.T) {
	if _, err := parseAssessment(`{"nq":{"view":"hausse","watch":"bornes"}}`); err == nil {
		t.Fatal("incomplete model output accepted")
	}
	for _, unsafe := range []string{"javascript:alert(1)", "https://someone:password@example.org"} {
		if _, _, err := manualContext("note", unsafe); err == nil {
			t.Errorf("unsafe source accepted: %s", unsafe)
		}
	}
}

func TestFedFreshDatedSources(t *testing.T) {
	cutoff := dateAt("2026-11-24T14:00:00Z")
	item := func(id, host, publication string) string {
		return `<item><title>Title &amp; ` + id + `</title><link><![CDATA[https://` + host + `/newsevents/pressreleases/monetary` + id + `.htm]]></link><pubDate>` + publication + `</pubDate></item>`
	}
	const official = "www.federalreserve.gov"
	one := item("a", official, "Mon, 23 Nov 2026 10:00:00 GMT")
	xml := `<rss><channel>` + one + one + item("b", official, "Tue, 24 Nov 2026 12:00:00 GMT") +
		item("c", official, "Sun, 22 Nov 2026 09:00:00 GMT") + item("d", official, "Wed, 18 Nov 2026 08:00:00 GMT") +
		item("old", official, "Tue, 17 Nov 2026 13:00:00 GMT") + item("future", official, "Tue, 24 Nov 2026 15:00:00 GMT") +
		item("offsite", "fed.invalid", "Tue, 24 Nov 2026 13:00:00 GMT") + `</channel></rss>`
	feed := parseFed([]byte(xml), cutoff)
	if !feed.available || len(feed.items) != 3 || feed.items[0].Title != "Title & b" || feed.items[1].Title != "Title & a" || feed.items[2].Title != "Title & c" {
		t.Fatalf("unexpected items: %+v", feed)
	}
	if feed.items[0].PublishedAt != "2026-11-24T12:00:00Z" {
		t.Fatalf("incorrect publication time: %s", feed.items[0].PublishedAt)
	}
	for _, invalid := range []string{`<html><item/></html>`, "<!DOCTYPE rss>" + xml, "<rss><channel><item>"} {
		if parseFed([]byte(invalid), cutoff).available {
			t.Errorf("invalid RSS accepted: %.50s", invalid)
		}
	}
}

func dateAt(date string) time.Time {
	result, err := time.Parse(time.RFC3339, date)
	if err != nil {
		panic(err)
	}
	return result
}

func barsAt(start string, count int) []int64 {
	startSec := dateAt(start).Unix()
	result := make([]int64, count)
	for i := range result {
		result[i] = startSec + int64(i)*300
	}
	return result
}

func seriesFixture(i instrument, cutoff time.Time, age int64, price float64, timestamps []int64) []byte {
	series := chartSeries{Timestamps: timestamps}
	series.Meta.Symbol = i.symbol + "=F"
	series.Meta.InstrumentType = "FUTURE"
	series.Meta.Currency = "USD"
	series.Meta.DataGranularity = "5m"
	series.Meta.RegularMarketPrice = price
	series.Meta.RegularMarketTime = cutoff.Unix() - age
	columns := barColumns{}
	reference := 20000.0
	if i == es {
		reference = 6000.0
	}
	for range timestamps {
		columns.Open = append(columns.Open, reference)
		columns.Low = append(columns.Low, reference-50)
		columns.High = append(columns.High, reference+50)
		columns.Close = append(columns.Close, reference+1)
	}
	series.Indicators.Quote = []barColumns{columns}
	data, err := json.Marshal(map[string]any{"chart": map[string]any{"result": []chartSeries{series}}})
	if err != nil {
		panic(err)
	}
	return data
}
