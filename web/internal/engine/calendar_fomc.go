package engine

import (
	"context"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const fomcCalendarURL = "https://www.federalreserve.gov/newsevents/calendar.htm"

var (
	fomcMonthHeading   = regexp.MustCompile(`(?is)<div\s+class=["']row-title text-center["']>\s*<h4\b[^>]*>([^<]+)</h4>`)
	fomcSectionHeading = regexp.MustCompile(`(?is)<div\s+class=["']row cal-nojs__rowTitle["']>\s*<h4\b[^>]*>([^<]+)</h4>`)
	fomcPanel          = regexp.MustCompile(`(?i)<div\s+class=["']panel-body["']>`)
	fomcColumn         = regexp.MustCompile(`(?is)<div\s+class=["']col-xs-(2|7|3)["']>\s*<p\b[^>]*>(.*?)</p>`)
	fomcInlineTag      = regexp.MustCompile(`<[^>]*>`)
)

func fetchFOMCCalendar(ctx context.Context, load fetchFunc, start, end time.Time) calendarFeed {
	result := calendarFeed{Name: "Fed", URL: fomcCalendarURL, Available: start.Before(end)}
	if !result.Available {
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	month := start.In(newYork)
	month = time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, newYork)
	// The month touched by the end boundary is excluded when it starts at end.
	for month.Before(end) && ctx.Err() == nil {
		events, available := fetchFOMCMonth(ctx, load, month, start, end)
		result.Events = append(result.Events, events...)
		result.Available = result.Available && available
		month = month.AddDate(0, 1, 0)
	}
	result.Available = result.Available && !month.Before(end)
	return result
}

func fetchFOMCMonth(ctx context.Context, load fetchFunc, month, start, end time.Time) ([]calendarEvent, bool) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	endpoint := fmt.Sprintf("https://www.federalreserve.gov/newsevents/%04d-%s.htm",
		month.Year(), strings.ToLower(month.Month().String()))
	body, err := load(ctx, endpoint, "text/html", 512_000)
	if err != nil {
		return nil, false
	}
	return parseFOMCMonth(string(body), month, start, end)
}

func parseFOMCMonth(page string, month, start, end time.Time) ([]calendarEvent, bool) {
	heading := fomcMonthHeading.FindStringSubmatch(page)
	if heading == nil || strings.TrimSpace(heading[1]) != month.Format("January 2006") {
		return nil, false
	}
	sections := fomcSectionHeading.FindAllStringSubmatchIndex(page, -1)
	if len(sections) == 0 {
		return nil, false
	}
	for i, section := range sections {
		if strings.TrimSpace(page[section[2]:section[3]]) != "FOMC Meetings" {
			continue
		}
		sectionEnd := len(page)
		if i+1 < len(sections) {
			sectionEnd = sections[i+1][0]
		}
		panels := fomcPanel.Split(page[section[1]:sectionEnd], -1)[1:]
		events := make([]calendarEvent, 0, len(panels))
		available := len(panels) != 0
		for _, panel := range panels {
			event, ok := parseFOMCPanel(panel, month)
			available = available && ok
			if ok && !event.At.Before(start) && event.At.Before(end) {
				events = append(events, event)
			}
		}
		return events, available
	}
	return nil, true
}

func parseFOMCPanel(panel string, month time.Time) (calendarEvent, bool) {
	fields := fomcColumn.FindAllStringSubmatch(panel, -1)
	if len(fields) != 3 || fields[0][1] != "2" || fields[1][1] != "7" || fields[2][1] != "3" {
		return calendarEvent{}, false
	}
	var title string
	switch fomcText(fields[1][2]) {
	case "FOMC Meeting", "FOMC Statement":
		title = "Décision du FOMC"
	case "FOMC Press Conference":
		title = "Conférence de presse du FOMC"
	case "FOMC Minutes":
		title = "Compte rendu du FOMC"
	default:
		return calendarEvent{}, false
	}
	clock := strings.ReplaceAll(fomcText(fields[0][2]), ".", "")
	parsed, err := time.Parse("3:04 PM", strings.ToUpper(clock))
	if err != nil {
		return calendarEvent{}, false
	}
	day, err := strconv.Atoi(fomcText(fields[2][2]))
	if err != nil || day < 1 || day > 31 {
		return calendarEvent{}, false
	}
	at := time.Date(month.Year(), month.Month(), day, parsed.Hour(), parsed.Minute(), 0, 0, newYork)
	if at.Month() != month.Month() {
		return calendarEvent{}, false
	}
	return calendarEvent{Title: title, At: at}, true
}

func fomcText(s string) string {
	return strings.Join(strings.Fields(html.UnescapeString(fomcInlineTag.ReplaceAllString(s, ""))), " ")
}
