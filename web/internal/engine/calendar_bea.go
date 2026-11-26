package engine

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"
)

const beaCalendarEndpoint = "https://www.bea.gov/news/schedule/ics/online-calendar-subscription.ics"
const beaCalendarPage = "https://www.bea.gov/news/schedule/full"

func fetchBEACalendar(ctx context.Context, load fetchFunc, start, end time.Time) calendarFeed {
	feed := calendarFeed{Name: "BEA", URL: beaCalendarPage}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	body, err := load(ctx, beaCalendarEndpoint, "text/calendar, text/plain", 300_000)
	if err != nil {
		return feed
	}
	releases, ok := parseCalendarICS(body, newYork)
	if !ok {
		return feed
	}
	for _, release := range releases {
		if release.At.Before(start) {
			continue
		}
		feed.Available = true
		if title := beaReleaseTitle(release.Summary); title != "" && release.At.Before(end) {
			feed.Events = append(feed.Events, calendarEvent{Title: title, At: release.At})
		}
	}
	return feed
}

func beaReleaseTitle(summary string) string {
	if strings.HasPrefix(summary, "personal income and outlays,") || summary == "personal income and outlays" {
		return "Revenus et dépenses des ménages (PCE)"
	}
	if !strings.HasPrefix(summary, "gdp (") && !strings.HasPrefix(summary, "gross domestic product,") &&
		!strings.HasPrefix(summary, "gross domestic product (") && summary != "gross domestic product" && summary != "gdp" {
		return ""
	}
	switch {
	case strings.Contains(summary, "(advance estimate)"):
		return "PIB américain (1re estimation)"
	case strings.Contains(summary, "(second estimate)"):
		return "PIB américain (2e estimation)"
	case strings.Contains(summary, "(third estimate)"):
		return "PIB américain (3e estimation)"
	default:
		return "PIB américain"
	}
}

type scheduledRelease struct {
	Summary string
	At      time.Time
}

var icsText = strings.NewReplacer(`\\`, `\`, `\,`, ",", `\;`, ";", `\n`, " ", `\N`, " ")

// parseCalendarICS reads the dated, explicit VEVENTs used by the BEA and BLS
// publication feeds. Untimed, recurring and unconfirmed events are omitted.
func parseCalendarICS(body []byte, floating *time.Location) ([]scheduledRelease, bool) {
	if !utf8.Valid(body) {
		return nil, false
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(strings.TrimPrefix(string(body), "\ufeff")), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if len(lines) > 0 && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) {
			lines[len(lines)-1] += line[1:]
			continue
		}
		lines = append(lines, line)
	}
	if len(lines) < 3 || lines[0] != "BEGIN:VCALENDAR" || lines[len(lines)-1] != "END:VCALENDAR" {
		return nil, false
	}

	var releases []scheduledRelease
	var event scheduledRelease
	var omit bool
	var depth int
	for _, line := range lines[1 : len(lines)-1] {
		switch {
		case depth == 0 && line == "BEGIN:VEVENT":
			event, omit, depth = scheduledRelease{}, false, 1
		case depth == 1 && line == "END:VEVENT":
			if !omit && event.Summary != "" && !event.At.IsZero() {
				releases = append(releases, event)
			}
			depth = 0
		case depth > 0 && strings.HasPrefix(line, "BEGIN:"):
			depth++
		case depth > 1 && strings.HasPrefix(line, "END:"):
			depth--
		case depth == 1:
			property, value, _ := strings.Cut(line, ":")
			name, params, _ := strings.Cut(property, ";")
			switch strings.ToUpper(name) {
			case "SUMMARY":
				event.Summary = strings.ToLower(strings.Join(strings.Fields(icsText.Replace(value)), " "))
			case "DTSTART":
				event.At = calendarTime(value, params, floating)
			case "STATUS":
				status := strings.ToUpper(strings.TrimSpace(value))
				omit = omit || status == "CANCELLED" || status == "TENTATIVE"
			case "RRULE", "RDATE", "RECURRENCE-ID":
				omit = true
			}
		}
	}
	return releases, depth == 0
}

func calendarTime(raw, params string, floating *time.Location) time.Time {
	const layout = "20060102T150405"
	location := floating
	var explicitZone bool
	for _, param := range strings.Split(params, ";") {
		key, value, _ := strings.Cut(param, "=")
		switch strings.ToUpper(key) {
		case "VALUE":
			if !strings.EqualFold(value, "DATE-TIME") {
				return time.Time{}
			}
		case "TZID":
			explicitZone = true
			switch strings.Trim(value, `"`) {
			case "America/New_York", "US/Eastern", "Eastern Standard Time":
				location = newYork
			case "UTC", "Etc/UTC":
				location = time.UTC
			default:
				return time.Time{}
			}
		}
	}
	if strings.HasSuffix(raw, "Z") && !explicitZone {
		at, _ := time.Parse(layout+"Z", raw)
		return at
	}
	if location == nil {
		return time.Time{}
	}
	at, err := time.ParseInLocation(layout, raw, location)
	if err != nil || at.Format(layout) != raw {
		return time.Time{}
	}
	return at
}
