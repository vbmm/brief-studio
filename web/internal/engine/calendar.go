package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

const calendarDays = 14

type calendarEvent struct {
	Title string    `json:"title"`
	At    time.Time `json:"scheduledAt"`
}

type calendarFeed struct {
	Name      string
	URL       string
	Available bool
	Partial   bool
	Events    []calendarEvent
}

type economicCalendar struct {
	Start, End time.Time
	Events     []calendarEvent
	Sources    []Source
	Missing    []string
}

func fetchCalendar(ctx context.Context, load fetchFunc, cutoff time.Time) economicCalendar {
	year, month, day := cutoff.In(newYork).Date()
	start := time.Date(year, month, day, 0, 0, 0, 0, newYork)
	end := start.AddDate(0, 0, calendarDays)
	fetchers := [...]func() calendarFeed{
		func() calendarFeed { return fetchBLSCalendar(ctx, load, start, end, cutoff) },
		func() calendarFeed { return fetchBEACalendar(ctx, load, start, end) },
		func() calendarFeed { return fetchFOMCCalendar(ctx, load, start, end) },
	}
	results := make([]chan calendarFeed, len(fetchers))
	for i, fetch := range fetchers {
		result := make(chan calendarFeed, 1)
		results[i] = result
		go func() { result <- fetch() }()
	}
	feeds := make([]calendarFeed, 0, len(fetchers))
	for _, result := range results {
		select {
		case feed := <-result:
			feeds = append(feeds, feed)
		case <-ctx.Done():
			return economicCalendar{}
		}
	}
	return combineCalendar(start, end, feeds)
}

func combineCalendar(start, end time.Time, feeds []calendarFeed) economicCalendar {
	calendar := economicCalendar{Start: start, End: end, Events: []calendarEvent{}}
	seen := make(map[calendarEvent]bool)
	for _, feed := range feeds {
		if !feed.Available || feed.Partial {
			calendar.Missing = append(calendar.Missing, feed.Name)
		}
		used := feed.Available
		for _, event := range feed.Events {
			if event.At.Before(start) || !event.At.Before(end) {
				continue
			}
			event.At = event.At.UTC()
			if seen[event] {
				continue
			}
			seen[event] = true
			used = true
			calendar.Events = append(calendar.Events, event)
		}
		if used {
			calendar.Sources = append(calendar.Sources, Source{Label: "Calendrier · " + feed.Name, URL: feed.URL})
		}
	}
	sort.Slice(calendar.Events, func(i, j int) bool {
		first, second := calendar.Events[i], calendar.Events[j]
		if first.At.Equal(second.At) {
			return first.Title < second.Title
		}
		return first.At.Before(second.At)
	})
	return calendar
}

func (calendar economicCalendar) writeSection(draft *strings.Builder, cutoff time.Time) {
	fmt.Fprintf(draft, "## Calendrier économique US\n\nHoraires annoncés du %s au %s, heure de New York (ET).\n\n",
		calendar.Start.Format("02/01/2006"), calendar.End.AddDate(0, 0, -1).Format("02/01/2006"))
	if len(calendar.Missing) != 0 {
		fmt.Fprintf(draft, "Couverture incomplète pour %s.\n\n", strings.Join(calendar.Missing, ", "))
	}
	if len(calendar.Events) == 0 {
		if len(calendar.Sources) == 0 {
			draft.WriteString("Calendrier indisponible.\n\n")
		} else {
			draft.WriteString("Aucune des publications suivies dans les calendriers consultés sur cette période.\n\n")
		}
		return
	}
	for _, event := range calendar.Events {
		status := "à venir"
		if !event.At.After(cutoff) {
			status = "horaire passé"
		}
		fmt.Fprintf(draft, "- **%s ET** %s · %s.\n", event.At.In(newYork).Format("02/01 à 15:04"), event.Title, status)
	}
	draft.WriteByte('\n')
}
