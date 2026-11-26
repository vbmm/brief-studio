package engine

import (
	"strings"
	"testing"
	"time"
)

func TestCalendarSortsDeduplicatesAndReportsPartialCoverage(t *testing.T) {
	cutoff := dateAt("2026-11-02T17:30:00Z")
	start := time.Date(2026, time.November, 2, 0, 0, 0, 0, newYork)
	end := start.AddDate(0, 0, calendarDays)
	employment := calendarEvent{"Rapport sur l’emploi (NFP)", dateAt("2026-11-02T13:30:00Z")}
	decision := calendarEvent{"Décision du FOMC", dateAt("2026-11-12T19:00:00Z")}
	calendar := combineCalendar(start, end, []calendarFeed{
		{Name: "BLS", URL: "https://www.bls.gov/schedule/", Available: true, Events: []calendarEvent{
			{"veille", start.Add(-time.Minute)}, {"hors période", end}, employment,
		}},
		{Name: "Fed", URL: "https://www.federalreserve.gov/newsevents/calendar.htm", Events: []calendarEvent{
			decision, {decision.Title, decision.At.In(newYork)},
		}},
	})
	if len(calendar.Events) != 2 || calendar.Events[0] != employment || calendar.Events[1] != decision || len(calendar.Sources) != 2 {
		t.Fatalf("unexpected merged calendar: %+v", calendar)
	}
	var text strings.Builder
	calendar.writeSection(&text, cutoff)
	for _, expected := range []string{
		"Couverture incomplète pour Fed", "02/11 à 08:30 ET", "horaire passé", "12/11 à 14:00 ET", "à venir",
	} {
		if !strings.Contains(text.String(), expected) {
			t.Errorf("missing %q in %s", expected, text.String())
		}
	}
}

func TestCalendarDistinguishesEmptyFromUnavailable(t *testing.T) {
	start := time.Date(2026, time.November, 26, 0, 0, 0, 0, newYork)
	end := start.AddDate(0, 0, calendarDays)
	for _, test := range []struct {
		available bool
		message   string
	}{
		{true, "Aucune des publications suivies"},
		{false, "Calendrier indisponible"},
	} {
		var text strings.Builder
		combineCalendar(start, end, []calendarFeed{{Name: "BEA", Available: test.available}}).writeSection(&text, start)
		if !strings.Contains(text.String(), test.message) {
			t.Fatalf("expected %q in %q", test.message, text.String())
		}
	}
}
