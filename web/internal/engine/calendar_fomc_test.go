package engine

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func fedCalendarPanel(clock, title, desc, date string) string {
	return fmt.Sprintf(`<div class="row">
 <div class="panel border panel-unstyled col-xs-12 col-sm-12 col-md-12">
  <div class="panel-body">
   <div class="row">
    <div class="col-xs-2"><p>%s</p></div>
    <div class="col-xs-7"><p>%s</p><p>%s</p></div>
    <div class="col-xs-3"><p>%s</p></div>
   </div>
  </div>
 </div>
</div>`, clock, title, desc, date)
}

func fedCalendarSection(title, panels string) string {
	return `<div class="row cal-nojs__rowTitle"><h4 class="col-md-12">` + title + ` </h4></div>
<div class="row"><div class="col-xs-2"><h6>Time:</h6></div><div class="col-xs-offset-7 col-xs-3"><h6>Release Date(s):</h6></div></div>` + panels
}

func fedCalendarPage(month, sections string) string {
	return `<div id="article" class='cal-nojs clearfix'>
<div class="col-xs-12 col-sm-8 col-md-8">
<div class="row-title text-center"><h4 class="text-center">` + month + `</h4></div>` + sections + `</div></div>`
}

func TestFOMCCalendarUsesTimedOfficialRows(t *testing.T) {
	december := fedCalendarPage("December 2026", fedCalendarSection("FOMC Meetings",
		fedCalendarPanel("2:00 p.m.", "FOMC Minutes", "<p>Meeting of December 8-9</p>", "30")+
			fedCalendarPanel("2:30 p.m.", `<a href="https://www.federalreserve.gov/live-broadcast.htm">FOMC Press Conference</a>`, "", "9")+
			fedCalendarPanel("2:00 p.m.", "FOMC Meeting", "<p>Two-day meeting, December&nbsp;8 - 9</p><p>Press Conference</p>", "9"))+
		fedCalendarSection("Other", fedCalendarPanel("2:00 p.m.", "FOMC Meeting", "Unrelated category", "8")))
	start := time.Date(2026, 11, 26, 0, 0, 0, 0, newYork)
	end := time.Date(2026, 12, 10, 0, 0, 0, 0, newYork)
	var endpoints []string
	load := func(_ context.Context, url, accept string, limit int64) ([]byte, error) {
		endpoints = append(endpoints, url)
		if accept != "text/html" || limit != 512_000 {
			t.Fatalf("request not bounded to HTML: %q %d", accept, limit)
		}
		switch url {
		case "https://www.federalreserve.gov/newsevents/2026-november.htm":
			return []byte(fedCalendarPage("November 2026", fedCalendarSection("Speeches", ""))), nil
		case "https://www.federalreserve.gov/newsevents/2026-december.htm":
			return []byte(december), nil
		default:
			return nil, errors.New("unexpected calendar endpoint")
		}
	}
	feed := fetchFOMCCalendar(context.Background(), load, start, end)
	if !feed.Available || feed.Name != "Fed" || feed.URL != fomcCalendarURL || len(endpoints) != 2 {
		t.Fatalf("unexpected feed metadata %+v, requested %v", feed, endpoints)
	}
	want := []calendarEvent{
		{Title: "Conférence de presse du FOMC", At: time.Date(2026, 12, 9, 14, 30, 0, 0, newYork)},
		{Title: "Décision du FOMC", At: time.Date(2026, 12, 9, 14, 0, 0, 0, newYork)},
	}
	if !reflect.DeepEqual(feed.Events, want) {
		t.Fatalf("got %+v, want %+v", feed.Events, want)
	}
}

func TestFOMCCalendarReportsPartialOrMissingSchedule(t *testing.T) {
	start := time.Date(2026, 9, 15, 0, 0, 0, 0, newYork)
	end := time.Date(2026, 10, 1, 0, 0, 0, 0, newYork)
	page := fedCalendarPage("September 2026", fedCalendarSection("FOMC Meetings",
		fedCalendarPanel("", "FOMC Meeting", "Time to be announced", "16")+
			fedCalendarPanel("2:30 p.m.", "FOMC Press Conference", "", "16")+
			fedCalendarPanel("2:00 p.m.", "FOMC Minutes", "Invalid date", "31")))
	load := func(_ context.Context, url, _ string, _ int64) ([]byte, error) {
		if url != "https://www.federalreserve.gov/newsevents/2026-september.htm" {
			t.Fatalf("queried outside window: %s", url)
		}
		return []byte(page), nil
	}
	feed := fetchFOMCCalendar(context.Background(), load, start, end)
	if feed.Available || len(feed.Events) != 1 || feed.Events[0].At.UTC().Format(time.RFC3339) != "2026-09-16T18:30:00Z" {
		t.Fatalf("invalid or missing dates changed the verified event: %+v", feed)
	}
	page = fedCalendarPage("October 2026", fedCalendarSection("Statistical Releases", ""))
	if feed := fetchFOMCCalendar(context.Background(), load, start, end); feed.Available || len(feed.Events) != 0 {
		t.Fatalf("different month mistaken for valid coverage: %+v", feed)
	}
	if feed := fetchFOMCCalendar(context.Background(), func(context.Context, string, string, int64) ([]byte, error) {
		return nil, errors.New("month not published")
	}, start, end); feed.Available || len(feed.Events) != 0 {
		t.Fatalf("unpublished month mistaken for valid coverage: %+v", feed)
	}
}
