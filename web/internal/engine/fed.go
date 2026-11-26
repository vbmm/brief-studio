package engine

import (
	"bytes"
	"context"
	"encoding/xml"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const fedEndpoint = "https://www.federalreserve.gov/feeds/press_monetary.xml"

type headline struct {
	Title       string `json:"title"`
	PublishedAt string `json:"publishedAt"`
	URL         string `json:"-"`
	date        time.Time
}

type fedFeed struct {
	available bool
	items     []headline
}

func fetchFed(ctx context.Context, load fetchFunc, cutoff time.Time) fedFeed {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	body, err := load(ctx, fedEndpoint, "application/rss+xml, application/xml, text/xml", 300_000)
	if err != nil {
		return fedFeed{}
	}
	return parseFed(body, cutoff)
}

func parseFed(body []byte, cutoff time.Time) fedFeed {
	if !utf8.Valid(body) {
		return fedFeed{}
	}
	upper := bytes.ToUpper(body)
	if bytes.Contains(upper, []byte("<!DOCTYPE")) || bytes.Contains(upper, []byte("<!ENTITY")) {
		return fedFeed{}
	}
	var rss struct {
		XMLName xml.Name `xml:"rss"`
		Channel *struct {
			Items []struct {
				Title string `xml:"title"`
				Link  string `xml:"link"`
				Date  string `xml:"pubDate"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal(body, &rss); err != nil || rss.Channel == nil {
		return fedFeed{}
	}
	items := []headline{}
	minDate := cutoff.Add(-7 * 24 * time.Hour)
	for _, row := range rss.Channel.Items {
		title := strings.Join(strings.Fields(row.Title), " ")
		if title == "" || utf8.RuneCountInString(title) > 300 {
			continue
		}
		date, err := parseFeedDate(row.Date)
		if err != nil || date.Before(minDate) || date.After(cutoff) {
			continue
		}
		link, err := url.Parse(strings.TrimSpace(row.Link))
		if err != nil || link.Scheme != "https" || link.Host != "www.federalreserve.gov" || link.User != nil ||
			link.RawQuery != "" || link.Fragment != "" || !strings.HasPrefix(link.Path, "/newsevents/pressreleases/monetary") ||
			!strings.HasSuffix(link.Path, ".htm") {
			continue
		}
		items = append(items, headline{Title: title, PublishedAt: date.UTC().Format(time.RFC3339), URL: link.String(), date: date})
	}
	sort.SliceStable(items, func(a, b int) bool { return items[a].date.After(items[b].date) })
	seen := map[string]bool{}
	result := fedFeed{available: true, items: []headline{}}
	for _, item := range items {
		if seen[item.URL] {
			continue
		}
		seen[item.URL] = true
		result.items = append(result.items, item)
		if len(result.items) == 3 {
			break
		}
	}
	return result
}

func parseFeedDate(raw string) (time.Time, error) {
	date := strings.TrimSpace(raw)
	if strings.HasSuffix(date, " GMT") || strings.HasSuffix(date, " UTC") {
		return time.Parse("Mon, 2 Jan 2006 15:04:05 MST", date)
	}
	return time.Parse("Mon, 2 Jan 2006 15:04:05 -0700", date)
}
