package sources

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestNormalizeXTarget(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{" @DeItaone , @Business, DEITAONE ", "deitaone,business"},
		{"one, two, three, four, five", "one,two,three,four,five"},
		{"single_handle", "single_handle"},
		{"", ""},
		{"https://x.com/DeItaone", ""},
		{"@DeItaone OR from:unwanted", ""},
		{"@DeItaone,-is:retweet", ""},
		{"deitaone,", ""},
		{"@@example", ""},
		{"un_nom_très_long", ""},
		{"over15characters", ""},
		{"one,two,three,four,five,six", ""},
	} {
		got, err := normalizeXTarget(test.input)
		if got != test.want || (err == nil) != (test.want != "") {
			t.Errorf("normalizeXTarget(%q) = %q, %v; want %q", test.input, got, err, test.want)
		}
	}
}

func TestXSingleRecentSearchAndVerifiedAuthors(t *testing.T) {
	const fixture = `{
		"data": [
			{"id":"101","author_id":"11","created_at":"2026-11-25T10:00:00Z","text":"First chosen account"},
			{"id":"102","author_id":"12","created_at":"2026-11-26T08:25:01-05:00","text":"More recent second account"},
			{"id":"102","author_id":"12","created_at":"2026-11-26T13:25:01Z","text":"Duplicate"},
			{"id":"103","author_id":"13","created_at":"2026-11-26T13:26:00Z","text":"Different account"},
			{"id":"104","author_id":"14","created_at":"2026-11-26T13:26:00Z","text":"Unverified author"},
			{"id":"105","author_id":"15","created_at":"2026-11-26T13:26:00Z","text":"Protected account"},
			{"id":"106","author_id":"11","created_at":"2026-11-25T01:29:59Z","text":"Outside time window"},
			{"id":"107","author_id":"11","created_at":"2026-11-26T13:30:01Z","text":"Future date"},
			{"id":"108/../../unsafe","author_id":"11","created_at":"2026-11-26T13:25:01Z","text":"Invalid identifier"},
			{"id":"109","author_id":"11","created_at":"not a timestamp","text":"Undated"}
		],
		"includes":{"users":[
			{"id":"11","username":"DeItaone","protected":false},
			{"id":"12","username":"Business","protected":false},
			{"id":"13","username":"other","protected":false},
			{"id":"15","username":"protected","protected":true}
		]},
		"meta":{"result_count":10,"next_token":"would-be-second-page"}
	}`
	cutoff := time.Date(2026, time.November, 26, 13, 30, 0, 0, time.UTC)
	calls := 0
	get := func(_ context.Context, requestURL, auth string, target any) error {
		calls++
		u, err := url.Parse(requestURL)
		if err != nil || u.Scheme != "https" || u.Host != "api.x.com" || u.Path != "/2/tweets/search/recent" {
			t.Fatal("unexpected provider endpoint")
		}
		if auth != "Bearer fixture%2F" || strings.Contains(requestURL, "fixture") {
			t.Fatal("synthetic authorization escaped its header")
		}
		for key, want := range map[string]string{
			"query": "(from:deitaone OR from:business OR from:protected)", "start_time": "2026-11-25T01:30:00Z",
			"max_results": "25", "sort_order": "recency", "tweet.fields": "created_at,author_id",
			"user.fields": "username,protected", "expansions": "author_id", "next_token": "",
		} {
			if got := u.Query().Get(key); got != want {
				t.Errorf("query %s = %q; want %q", key, got, want)
			}
		}
		return json.Unmarshal([]byte(fixture), target)
	}
	items, err := fetchX(context.Background(), get, Settings{
		Target: "@DeItaone, Business, PROTECTED", Token: "Bearer fixture%2F",
	}, cutoff)
	if err != nil || len(items) != 2 || calls != 1 {
		t.Fatalf("wanted two posts from one call; got %d, %v, calls %d", len(items), err, calls)
	}
	if items[0].URL != "https://x.com/business/status/102" || items[0].Text != "More recent second account" ||
		items[0].PublishedAt.Format(time.RFC3339) != "2026-11-26T13:25:01Z" || items[1].URL != "https://x.com/deitaone/status/101" {
		t.Fatalf("unexpected sorted evidence: %#v", items)
	}
}

func TestXDistinguishesEmptyAndIncompleteResponses(t *testing.T) {
	for _, tc := range []struct {
		fixture string
		ok      bool
	}{
		{`{"meta":{"result_count":0}}`, true},
		{`{"meta":{"result_count":1}}`, false},
		{`{}`, false},
		{`{"errors":[{"detail":"raw provider details"}],"meta":{"result_count":0}}`, false},
		{`{"data":[{"id":"101","author_id":"11","created_at":"2026-11-26T13:25:00Z","text":"Unconfirmed"}],"meta":{"result_count":1}}`, false},
	} {
		get := func(_ context.Context, _, _ string, destination any) error {
			return json.Unmarshal([]byte(tc.fixture), destination)
		}
		items, err := fetchX(context.Background(), get, Settings{Target: "deitaone", Token: "fixture"}, time.Now())
		if (err == nil) != tc.ok || len(items) != 0 || err != nil && !errors.Is(err, errXInvalidPayload) {
			t.Errorf("unexpected empty/incomplete response classification: got %v, %v", items, err)
		}
	}
}

func TestXDoesNotRetryOrFetchInvalidConfiguration(t *testing.T) {
	calls := 0
	providerError := sourceError{429, "La limite de requêtes du fournisseur est atteinte."}
	get := func(_ context.Context, _, _ string, _ any) error {
		calls++
		return providerError
	}
	if _, err := fetchX(context.Background(), get, Settings{Target: "deitaone", Token: "fixture\r\nHeader: unsafe"}, time.Now()); err == nil || calls != 0 {
		t.Fatalf("invalid token accepted or fetched: %v, calls %d", err, calls)
	}
	if _, err := fetchX(context.Background(), get, Settings{Target: "deitaone", Token: "fixture"}, time.Now()); !errors.Is(err, providerError) || calls != 1 {
		t.Fatalf("provider error changed or retried: %v, calls %d", err, calls)
	}
}
