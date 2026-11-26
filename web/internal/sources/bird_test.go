package sources

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestBirdSnapshotOnlyRetainsRecentPublicWalterReferences(t *testing.T) {
	now := time.Date(2026, 11, 26, 11, 0, 0, 0, time.UTC)
	input := `[
		{"id":"1101","createdAt":"Wed Nov 25 18:42:03 +0000 2026","author":{"username":"DeItaone"},"text":"fixture must not be copied"},
		{"id":"1101","createdAt":"2026-11-25T18:42:03Z","author":{"username":"deitaone"}},
		{"id":"1102","createdAt":"2026-11-24T18:00:00Z","author":{"username":"deitaone"}},
		{"id":"1103","createdAt":"2026-11-26T12:00:00Z","author":{"username":"deitaone"}},
		{"id":"1104","createdAt":"2026-11-26T10:00:00Z","author":{"username":"other"}},
		{"id":"1105","createdAt":"2026-11-26T10:00:00Z","author":{"username":"deitaone","protected":true}}
	]`
	snapshot, err := parseBirdSnapshot([]byte(input), now)
	if err != nil || len(snapshot.Posts) != 1 || snapshot.Posts[0].ID != "1101" || snapshot.Posts[0].Account != walterHandle || snapshot.Validate(now) != nil {
		t.Fatalf("unexpected Bird snapshot: %+v, %v", snapshot, err)
	}
	if got := snapshot.Headlines(now)[0]; got.Text != "" || got.URL != "https://x.com/deitaone/status/1101" {
		t.Fatalf("unexpected reference: %+v", got)
	}
	for _, empty := range []string{"[]", `[{"author":{"username":"other"}}]`, "not json"} {
		if _, err := parseBirdSnapshot([]byte(empty), now); err == nil {
			t.Fatal("unexpected or empty output must not replace the saved snapshot")
		}
	}
}

func TestBirdUsesChildPathWithoutAmbientPersonalTokens(t *testing.T) {
	got := birdChromeEnvironment([]string{"AUTH_TOKEN=fixture", "CT0=fixture", "OPENAI_API_KEY=fixture", "PATH=/usr/bin", "LANG=fr_FR.UTF-8"}, "/opt/homebrew/bin")
	if len(got) != 2 || !slices.Contains(got, "LANG=fr_FR.UTF-8") || !strings.HasPrefix(got[1], "PATH=/opt/homebrew/bin") {
		t.Fatalf("unexpected child environment: %v", got)
	}
}
