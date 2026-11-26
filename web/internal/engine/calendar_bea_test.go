package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestBEACalendarUsesTimedNationalReleases(t *testing.T) {
	fixture := strings.Join([]string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"BEGIN:VEVENT",
		"SUMMARY:Gross Domestic Product\\, 4th Quarter (Advance Estima",
		" te)",
		"DTSTART;VALUE=DATE-TIME:20260305T133000Z",
		"END:VEVENT",
		"BEGIN:VEVENT",
		"SUMMARY:Personal Income and Outlays\\, January 2026",
		"DTSTART:20260309T123000Z",
		"END:VEVENT",
		"BEGIN:VEVENT",
		"SUMMARY:GDP by County and Personal Income by County\\, 2025",
		"DTSTART:20260309T133000Z",
		"END:VEVENT",
		"BEGIN:VEVENT",
		"SUMMARY:GDP (Second Estimate)",
		"DTSTART:20260310T040000Z",
		"END:VEVENT",
		"END:VCALENDAR",
	}, "\r\n")
	start := time.Date(2026, 3, 5, 0, 0, 0, 0, newYork)
	end := start.AddDate(0, 0, 5)
	load := func(ctx context.Context, endpoint, accept string, limit int64) ([]byte, error) {
		if endpoint != beaCalendarEndpoint || !strings.Contains(accept, "text/calendar") || limit <= 0 {
			t.Fatalf("unexpected request %q %q %d", endpoint, accept, limit)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("expected a feed deadline")
		}
		return []byte(fixture), nil
	}
	feed := fetchBEACalendar(context.Background(), load, start, end)
	if !feed.Available || feed.Name != "BEA" || feed.URL != beaCalendarPage || len(feed.Events) != 2 {
		t.Fatalf("unexpected BEA coverage: %+v", feed)
	}
	for index, want := range []struct{ title, localTime string }{
		{"PIB américain (1re estimation)", "2026-03-05 08:30 EST"},
		{"Revenus et dépenses des ménages (PCE)", "2026-03-09 08:30 EDT"},
	} {
		got := feed.Events[index]
		if got.Title != want.title || got.At.In(newYork).Format("2006-01-02 15:04 MST") != want.localTime {
			t.Fatalf("event %d = %+v, expected %+v", index, got, want)
		}
	}
	if empty := fetchBEACalendar(context.Background(), load, start.AddDate(0, 0, 1), start.AddDate(0, 0, 2)); !empty.Available || len(empty.Events) != 0 {
		t.Fatalf("future source should be available with an empty window: %+v", empty)
	}
	if stale := fetchBEACalendar(context.Background(), load, end.AddDate(0, 0, 1), end.AddDate(0, 0, 2)); stale.Available {
		t.Fatalf("source without future coverage should be unavailable: %+v", stale)
	}
}

func TestBEACalendarReportsFetchAndParseFailures(t *testing.T) {
	start := time.Date(2026, 11, 25, 0, 0, 0, 0, newYork)
	for _, test := range []struct {
		name string
		body string
		err  error
	}{
		{"request failure", "", errors.New("network error")},
		{"HTML challenge", "<!DOCTYPE html><html></html>", nil},
		{"unfinished event", "BEGIN:VCALENDAR\nBEGIN:VEVENT\nEND:VCALENDAR", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			load := func(context.Context, string, string, int64) ([]byte, error) { return []byte(test.body), test.err }
			if feed := fetchBEACalendar(context.Background(), load, start, start.AddDate(0, 0, 7)); feed.Available || len(feed.Events) != 0 {
				t.Fatalf("expected unavailable source: %+v", feed)
			}
		})
	}
}

func TestCalendarICSOmitsUntimedAndUnconfirmedEvents(t *testing.T) {
	fixture := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
SUMMARY:Employment Situation
DTSTART;TZID=US/Eastern:20261106T083000
BEGIN:VALARM
SUMMARY:Reminder
END:VALARM
END:VEVENT
BEGIN:VEVENT
SUMMARY:All-day event
DTSTART;VALUE=DATE:20261105
END:VEVENT
BEGIN:VEVENT
SUMMARY:No time zone
DTSTART:20261106T083000
END:VEVENT
BEGIN:VEVENT
SUMMARY:Postponed event
DTSTART:20261106T133000Z
STATUS:CANCELLED
END:VEVENT
BEGIN:VEVENT
SUMMARY:Preliminary event
DTSTART:20261106T133000Z
STATUS:TENTATIVE
END:VEVENT
BEGIN:VEVENT
SUMMARY:Recurring event
DTSTART:20261106T133000Z
RRULE:FREQ=YEARLY
END:VEVENT
END:VCALENDAR
`
	releases, ok := parseCalendarICS([]byte(fixture), nil)
	if want := time.Date(2026, 11, 6, 13, 30, 0, 0, time.UTC); !ok || len(releases) != 1 || releases[0].Summary != "employment situation" || !releases[0].At.Equal(want) {
		t.Fatalf("unexpected dated releases: %v %+v", ok, releases)
	}
}
