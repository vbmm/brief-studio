package engine

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"
)

const (
	blsCalendarURL = "https://www.bls.gov/schedule/"
	blsCalendarICS = "https://www.bls.gov/schedule/news_release/bls.ics"
	blsScheduleURL = "https://www.bls.gov/schedule/news_release/"
	openCountURL   = "https://theopencount.com/calendar/"
	openCountAPI   = "https://theopencount.com/api/calendar"
)

var blsReleases = [...]struct{ prefix, title string }{
	{"consumer price index", "Inflation CPI"},
	{"producer price index", "Prix à la production (PPI)"},
	{"producer price indexes", "Prix à la production (PPI)"},
	{"employment situation", "Rapport sur l’emploi (NFP)"},
	{"the employment situation", "Rapport sur l’emploi (NFP)"},
	{"job openings and labor turnover survey", "Offres d’emploi (JOLTS)"},
	{"employment cost index", "Coût du travail (ECI)"},
}

func fetchBLSCalendar(ctx context.Context, load fetchFunc, start, end, cutoff time.Time) calendarFeed {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	officialCtx, stopOfficial := context.WithTimeout(ctx, 3*time.Second)
	feed := fetchOfficialBLSCalendar(officialCtx, load, start, end)
	stopOfficial()
	if feed.Available || ctx.Err() != nil {
		return feed
	}
	body, err := load(ctx, openCountAPI, "application/json", 256<<10)
	if err == nil {
		fallback := parseOpenCountBLS(body, start, end, cutoff)
		if fallback.Available {
			return fallback
		}
	}
	return feed
}

func fetchOfficialBLSCalendar(ctx context.Context, load fetchFunc, start, end time.Time) calendarFeed {
	feed := calendarFeed{Name: "BLS", URL: blsCalendarURL}
	body, err := load(ctx, blsCalendarICS, "text/calendar", 2<<20)
	if err != nil {
		return feed
	}
	// The official schedule specifies Eastern Time for local timestamps.
	rows, valid := parseCalendarICS(body, newYork)
	if !valid {
		return feed
	}
	for _, row := range rows {
		if row.At.Before(start) {
			continue
		}
		// An older feed with no timed entries after the start cannot establish coverage.
		feed.Available = true
		if !row.At.Before(end) {
			continue
		}
		if title := blsReleaseTitle(row.Summary); title != "" {
			feed.Events = append(feed.Events, calendarEvent{Title: title, At: row.At})
		}
	}
	return feed
}

// The publisher exposes this calendar separately from its data API. Use only
// its timetable and keep the publisher visible in the brief's sources.
func parseOpenCountBLS(body []byte, start, end, cutoff time.Time) calendarFeed {
	type entry struct {
		At     time.Time `json:"date"`
		Name   string    `json:"name"`
		Agency string    `json:"agency"`
		Source string    `json:"src"`
	}
	var snapshot struct {
		Version int       `json:"v"`
		At      time.Time `json:"at"`
		Items   []entry   `json:"items"`
		Recent  []entry   `json:"recent"`
		Log     []string  `json:"log"`
	}
	if json.Unmarshal(body, &snapshot) != nil || snapshot.Version != 3 ||
		snapshot.At.IsZero() || snapshot.At.After(cutoff) || cutoff.Sub(snapshot.At) > 24*time.Hour ||
		snapshot.Items == nil || snapshot.Recent == nil || !slices.Contains(snapshot.Log, "BLS schedule read") {
		return calendarFeed{}
	}
	feed := calendarFeed{Name: "BLS via The Open Count", URL: openCountURL, Partial: true}
	for _, rows := range [][]entry{snapshot.Items, snapshot.Recent} {
		for _, row := range rows {
			title := openCountBLSTitle(row.Name)
			if row.Agency != "BLS" || title == "" {
				continue
			}
			_, offset := row.At.Zone()
			_, easternOffset := row.At.In(newYork).Zone()
			if row.Source != blsScheduleURL || row.At.IsZero() || offset != easternOffset {
				return calendarFeed{}
			}
			if row.At.Before(start) {
				continue
			}
			feed.Available = true
			if row.At.Before(end) {
				feed.Events = append(feed.Events, calendarEvent{Title: title, At: row.At})
			}
		}
	}
	return feed
}

func openCountBLSTitle(name string) string {
	switch name {
	case "Consumer prices (CPI)":
		return "Inflation CPI"
	case "Producer prices (PPI)":
		return "Prix à la production (PPI)"
	case "Jobs report":
		return "Rapport sur l’emploi (NFP)"
	case "Job openings (JOLTS)":
		return "Offres d’emploi (JOLTS)"
	default:
		return ""
	}
}

func blsReleaseTitle(raw string) string {
	summary := strings.ToLower(strings.Join(strings.Fields(raw), " "))
	for _, release := range blsReleases {
		suffix, found := strings.CutPrefix(summary, release.prefix)
		if found && (suffix == "" || strings.IndexAny(suffix, " ,(:–—-") == 0) {
			return release.title
		}
	}
	return ""
}
