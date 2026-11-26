package sources

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	xSearchURL   = "https://api.x.com/2/tweets/search/recent"
	xResultLimit = 25
	xMaxAge      = 36 * time.Hour
)

var (
	xHandle            = regexp.MustCompile(`^[A-Za-z0-9_]{1,15}$`)
	xID                = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)
	errXInvalidTarget  = errors.New("Saisis de un à cinq comptes X séparés par des virgules, par exemple @DeItaone, @business.")
	errXInvalidPayload = errors.New("L’API X a renvoyé une réponse incomplète ou inattendue.")
)

func normalizeXTarget(target string) (string, error) {
	parts := strings.Split(target, ",")
	if len(parts) > 5 {
		return "", errXInvalidTarget
	}
	handles := make([]string, 0, len(parts))
	for _, part := range parts {
		handle := strings.TrimPrefix(strings.TrimSpace(part), "@")
		if !xHandle.MatchString(handle) {
			return "", errXInvalidTarget
		}
		handle = strings.ToLower(handle)
		if !slices.Contains(handles, handle) {
			handles = append(handles, handle)
		}
	}
	return strings.Join(handles, ","), nil
}

func fetchX(ctx context.Context, get getter, settings Settings, cutoff time.Time) ([]Headline, error) {
	settings, err := Normalize(X, settings)
	if err != nil {
		return nil, err
	}
	handles := strings.Split(settings.Target, ",")
	clauses := make([]string, 0, len(handles))
	for _, handle := range handles {
		clauses = append(clauses, "from:"+handle)
	}
	query := clauses[0]
	if len(clauses) > 1 {
		query = "(" + strings.Join(clauses, " OR ") + ")"
	}
	start := cutoff.Add(-xMaxAge)
	params := url.Values{
		"query":        {query},
		"start_time":   {start.UTC().Format(time.RFC3339)},
		"max_results":  {strconv.Itoa(xResultLimit)},
		"sort_order":   {"recency"},
		"tweet.fields": {"created_at,author_id"},
		"user.fields":  {"username,protected"},
		"expansions":   {"author_id"},
	}
	var response xSearchResponse
	if err := get(ctx, xSearchURL+"?"+params.Encode(), "Bearer "+settings.Token, &response); err != nil {
		return nil, err
	}
	return xHeadlines(response, handles, start, cutoff)
}

type xSearchResponse struct {
	Data []struct {
		ID        string `json:"id"`
		Text      string `json:"text"`
		AuthorID  string `json:"author_id"`
		CreatedAt string `json:"created_at"`
	} `json:"data"`
	Includes struct {
		Users []struct {
			ID        string `json:"id"`
			Username  string `json:"username"`
			Protected bool   `json:"protected"`
		} `json:"users"`
	} `json:"includes"`
	Meta struct {
		ResultCount *int `json:"result_count"`
	} `json:"meta"`
	Errors []json.RawMessage `json:"errors"`
}

func xHeadlines(response xSearchResponse, handles []string, start, cutoff time.Time) ([]Headline, error) {
	if len(response.Errors) != 0 || response.Meta.ResultCount == nil ||
		*response.Meta.ResultCount != len(response.Data) || len(response.Data) > xResultLimit {
		return nil, errXInvalidPayload
	}
	authors := make(map[string]string, len(response.Includes.Users))
	for _, user := range response.Includes.Users {
		handle := strings.ToLower(user.Username)
		if user.Protected || !xID.MatchString(user.ID) || !slices.Contains(handles, handle) {
			continue
		}
		if other, exists := authors[user.ID]; exists && other != handle {
			return nil, errXInvalidPayload
		}
		authors[user.ID] = handle
	}
	if len(response.Data) != 0 && len(authors) == 0 {
		return nil, errXInvalidPayload
	}
	headlines := make([]Headline, 0, len(response.Data))
	seen := make(map[string]bool, len(response.Data))
	for _, post := range response.Data {
		handle, confirmed := authors[post.AuthorID]
		if !confirmed || !xID.MatchString(post.ID) || seen[post.ID] {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, post.CreatedAt)
		if err != nil || at.Before(start) || at.After(cutoff) || strings.TrimSpace(post.Text) == "" {
			continue
		}
		seen[post.ID] = true
		headlines = append(headlines, Headline{
			Text: post.Text, URL: "https://x.com/" + handle + "/status/" + post.ID, PublishedAt: at.UTC(),
		})
	}
	slices.SortStableFunc(headlines, func(a, b Headline) int { return b.PublishedAt.Compare(a.PublishedAt) })
	return headlines, nil
}
