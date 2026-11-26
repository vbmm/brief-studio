package sources

import (
	"errors"
	"slices"
	"strings"
	"time"
)

const XSnapshotSchema = "brief-studio/x-snapshot/v1"

type XSnapshot struct {
	Schema     string          `json:"schema"`
	ObservedAt time.Time       `json:"observedAt"`
	Posts      []XSnapshotPost `json:"posts"`
}

type XSnapshotPost struct {
	ID          string    `json:"id"`
	Account     string    `json:"account"`
	PublishedAt time.Time `json:"publishedAt"`
}

func (snapshot XSnapshot) Validate(now time.Time) error {
	invalid := errors.New("Le fichier X ne contient pas un instantané valide.")
	if snapshot.Schema != XSnapshotSchema || snapshot.ObservedAt.IsZero() || snapshot.ObservedAt.After(now) ||
		snapshot.Posts == nil || len(snapshot.Posts) > xResultLimit {
		return invalid
	}
	for _, post := range snapshot.Posts {
		if !xID.MatchString(post.ID) || !xHandle.MatchString(post.Account) || post.PublishedAt.IsZero() ||
			post.PublishedAt.After(snapshot.ObservedAt) {
			return invalid
		}
	}
	return nil
}

func (snapshot XSnapshot) Stale(now time.Time) bool {
	return snapshot.ObservedAt.Before(now.Add(-xMaxAge))
}

func (snapshot XSnapshot) Headlines(now time.Time) []Headline {
	if snapshot.Stale(now) {
		return nil
	}
	var items []Headline
	seen := make(map[string]bool)
	start := now.Add(-xMaxAge)
	for _, post := range snapshot.Posts {
		if seen[post.ID] || post.PublishedAt.Before(start) {
			continue
		}
		seen[post.ID] = true
		items = append(items, Headline{URL: "https://x.com/" + strings.ToLower(post.Account) + "/status/" + post.ID,
			PublishedAt: post.PublishedAt.UTC()})
	}
	slices.SortStableFunc(items, func(a, b Headline) int { return b.PublishedAt.Compare(a.PublishedAt) })
	return items
}
