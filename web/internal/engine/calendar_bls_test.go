package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestBLSCalendarSelectsTimedNationalReleases(t *testing.T) {
	// These examples include both sides of the 2026 change to standard time.
	entries := []string{
		"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//Fixture//EN",
		"BEGIN:VEVENT", "UID:eci", "DTSTART;TZID=America/New_York:20261030T083000", "SUMMARY:Employment Cost Index for Third Quarter 2026", "END:VEVENT",
		"BEGIN:VEVENT", "UID:nfp", "DTSTART:20261106T133000Z", "SUMMARY:Employment Situation for October 2026", "END:VEVENT",
		"BEGIN:VEVENT", "UID:cpi", "DTSTART:20261110T083000", "SUMMARY:Consumer Price Index for October 2026", "END:VEVENT",
		"BEGIN:VEVENT", "UID:jolts", "DTSTART;TZID=US/Eastern:20261103T100000", "SUMMARY:Job Openings and Labor Turnover Su", " rvey for September 2026", "END:VEVENT",
		"BEGIN:VEVENT", "UID:ppi", "DTSTART;TZID=America/New_York:20261113T083000", "SUMMARY:Producer Price Indexes for October 2026", "END:VEVENT",
		"BEGIN:VEVENT", "UID:state", "DTSTART:20261106T163000Z", "SUMMARY:State Job Openings and Labor Turnover for October 2026", "END:VEVENT",
		"BEGIN:VEVENT", "UID:holiday", "DTSTART;VALUE=DATE:20261111", "SUMMARY:Veterans Day", "END:VEVENT",
		"BEGIN:VEVENT", "UID:cancelled", "DTSTART:20261112T133000Z", "SUMMARY:Consumer Price Index", "STATUS:CANCELLED", "END:VEVENT",
		"BEGIN:VEVENT", "UID:tentative", "DTSTART:20261112T133000Z", "SUMMARY:Consumer Price Index", "STATUS:TENTATIVE", "END:VEVENT",
		"BEGIN:VEVENT", "UID:end", "DTSTART;TZID=America/New_York:20261114T000000", "SUMMARY:Consumer Price Index", "END:VEVENT",
		"END:VCALENDAR", "",
	}
	start := time.Date(2026, 10, 30, 0, 0, 0, 0, newYork)
	end := time.Date(2026, 11, 14, 0, 0, 0, 0, newYork)
	load := func(ctx context.Context, endpoint, accept string, limit int64) ([]byte, error) {
		if _, bounded := ctx.Deadline(); !bounded || endpoint != blsCalendarICS || accept != "text/calendar" || limit <= 0 {
			t.Fatalf("unexpected BLS request: %s, %s, %d", endpoint, accept, limit)
		}
		return []byte(strings.Join(entries, "\r\n")), nil
	}
	feed := fetchBLSCalendar(context.Background(), load, start, end, start)
	if !feed.Available || feed.Name != "BLS" || feed.URL != blsCalendarURL || len(feed.Events) != 5 {
		t.Fatalf("unexpected feed: %+v", feed)
	}
	want := map[string]string{
		"Coût du travail (ECI)":      "2026-10-30T12:30:00Z",
		"Rapport sur l’emploi (NFP)": "2026-11-06T13:30:00Z",
		"Inflation CPI":              "2026-11-10T13:30:00Z",
		"Offres d’emploi (JOLTS)":    "2026-11-03T15:00:00Z",
		"Prix à la production (PPI)": "2026-11-13T13:30:00Z",
	}
	for _, event := range feed.Events {
		if got := event.At.UTC().Format(time.RFC3339); got != want[event.Title] {
			t.Fatalf("%s at %s, want %s", event.Title, got, want[event.Title])
		}
		delete(want, event.Title)
	}
}

func TestBLSCalendarAvailability(t *testing.T) {
	start := time.Date(2026, 11, 26, 0, 0, 0, 0, newYork)
	end := start.AddDate(0, 0, 15)
	for _, tc := range []struct {
		name, startValue string
		forbidden        bool
		available        bool
	}{
		{name: "forbidden", forbidden: true},
		{name: "malformed"},
		{name: "expired", startValue: "20260510T133000Z"},
		{name: "available with no matching release", startValue: "20261210T133000Z", available: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			load := func(context.Context, string, string, int64) ([]byte, error) {
				if tc.forbidden {
					return nil, errors.New("statut du flux 403")
				}
				if tc.startValue == "" {
					return []byte("access denied"), nil
				}
				return []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Fixture//EN\r\nBEGIN:VEVENT\r\nUID:x\r\nDTSTART:" + tc.startValue + "\r\nSUMMARY:State Unemployment for November 2026\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"), nil
			}
			got := fetchBLSCalendar(context.Background(), load, start, end, start)
			if got.Available != tc.available || len(got.Events) != 0 || got.URL != blsCalendarURL {
				t.Fatalf("unexpected feed: %+v", got)
			}
		})
	}
}

const openCountFixture = `{
	"v": 3,
	"at": "2026-11-26T07:07:00.415Z",
	"items": [
		{"date":"2026-12-01T10:00:00-05:00","name":"Job openings (JOLTS)","agency":"BLS","src":"https://www.bls.gov/schedule/news_release/"},
		{"date":"2026-12-04T08:30:00-05:00","name":"Jobs report","agency":"BLS","src":"https://www.bls.gov/schedule/news_release/"},
		{"date":"2026-12-10T08:30:00-05:00","name":"Consumer prices (CPI)","agency":"BLS","src":"https://www.bls.gov/schedule/news_release/"},
		{"date":"2026-12-15T08:30:00-05:00","name":"Producer prices (PPI)","agency":"BLS","src":"https://www.bls.gov/schedule/news_release/"},
		{"date":"2026-12-10T08:30:00-05:00","name":"Real earnings","agency":"BLS","src":"https://www.bls.gov/schedule/news_release/"},
		{"date":"2026-12-08T13:30:00+00:00","name":"Trade balance","agency":"BEA","src":"https://www.bea.gov/news/schedule"}
	],
	"recent": [
		{"date":"2026-11-10T08:30:00-05:00","name":"Consumer prices (CPI)","agency":"BLS","src":"https://www.bls.gov/schedule/news_release/"}
	],
	"log": ["BEA schedule read", "BLS schedule read"]
}`

func TestBLSCalendarFallsBackWithAttribution(t *testing.T) {
	start := time.Date(2026, 11, 26, 0, 0, 0, 0, newYork)
	end := start.AddDate(0, 0, 14)
	cutoff := start.Add(6 * time.Hour)
	var requests []string
	load := func(ctx context.Context, endpoint, accept string, limit int64) ([]byte, error) {
		requests = append(requests, endpoint)
		if endpoint == blsCalendarICS {
			return nil, errors.New("statut du flux 403")
		}
		if endpoint != openCountAPI || accept != "application/json" || limit <= 0 {
			t.Fatalf("unexpected calendar request to %s", endpoint)
		}
		return []byte(openCountFixture), nil
	}
	got := fetchBLSCalendar(context.Background(), load, start, end, cutoff)
	if strings.Join(requests, ",") != blsCalendarICS+","+openCountAPI || !got.Available || !got.Partial ||
		got.Name != "BLS via The Open Count" || got.URL != openCountURL || len(got.Events) != 2 {
		t.Fatalf("unexpected attributed fallback: %+v, requests: %v", got, requests)
	}
	if got.Events[0].At.UTC().Format(time.RFC3339) != "2026-12-01T15:00:00Z" || got.Events[0].Title != "Offres d’emploi (JOLTS)" ||
		got.Events[1].At.UTC().Format(time.RFC3339) != "2026-12-04T13:30:00Z" || got.Events[1].Title != "Rapport sur l’emploi (NFP)" {
		t.Fatalf("incorrect jobs calendar: %+v", got.Events)
	}
}

func TestBLSCalendarFallbackRejectsUnverifiedSnapshots(t *testing.T) {
	start := time.Date(2026, 11, 26, 0, 0, 0, 0, newYork)
	end := start.AddDate(0, 0, 14)
	cutoff := start.Add(6 * time.Hour)
	for _, tc := range []struct{ name, old, new string }{
		{"unknown version", `"v": 3`, `"v": 2`},
		{"stale snapshot", "2026-11-26T07:07:00.415Z", "2026-11-25T10:07:00.415Z"},
		{"future snapshot", "2026-11-26T07:07:00.415Z", "2026-11-26T11:07:00.415Z"},
		{"missing BLS success", "BLS schedule read", "BLS schedule failed"},
		{"wrong source", "https://www.bls.gov/schedule/news_release/", "https://example.org/schedule/news_release/"},
		{"wrong eastern offset", "2026-12-01T10:00:00-05:00", "2026-12-01T10:00:00-04:00"},
		{"missing upcoming releases", `"items": [`, `"other": [`},
		{"missing recent releases", `"recent": [`, `"other": [`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(strings.Replace(openCountFixture, tc.old, tc.new, 1))
			feed := parseOpenCountBLS(body, start, end, cutoff)
			if feed.Available || len(feed.Events) != 0 {
				t.Fatalf("unverified fallback accepted: %+v", feed)
			}
		})
	}
}
