package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

const (
	fixtureDiscordChannel = "1173001000000000001"
	fixtureDiscordGuild   = "1173001000000000002"
	fixtureDiscordMessage = "1173001000000000003"
)

func TestNormalizeDiscordTarget(t *testing.T) {
	base := discordLinks + fixtureDiscordGuild + "/" + fixtureDiscordChannel
	for _, example := range []struct{ input, output string }{
		{" " + fixtureDiscordChannel + " ", fixtureDiscordChannel},
		{base + "/", base},
		{base + "/" + fixtureDiscordMessage, base},
		{"http://discord.com/channels/" + fixtureDiscordGuild + "/" + fixtureDiscordChannel, ""},
		{"https://discord.com.evil.example/channels/" + fixtureDiscordGuild + "/" + fixtureDiscordChannel, ""},
		{"https://staff@discord.com/channels/" + fixtureDiscordGuild + "/" + fixtureDiscordChannel, ""},
		{"https://discord.com/channels/@me/" + fixtureDiscordChannel, ""},
		{base + "?redirect=https://example.org", ""},
		{"18446744073709551616", ""},
		{"01173001000000000001", ""},
	} {
		got, err := normalizeDiscordTarget(example.input)
		if got != example.output || (err != nil) != (example.output == "") {
			t.Errorf("normalize(%q) = %q, %v; want %q", example.input, got, err, example.output)
		}
	}
}

func TestDiscordBoundedReadAndFreshSafeExcerpts(t *testing.T) {
	endpoint := discordAPI + "/channels/" + fixtureDiscordChannel
	cutoff := time.Date(2026, time.November, 26, 13, 0, 0, 0, time.UTC)
	news := func(id, at, text, extra string) string {
		return fmt.Sprintf(`{"id":%q,"channel_id":%q,"timestamp":%q,"content":%q%s}`, id, fixtureDiscordChannel, at, text, extra)
	}
	old := news("1173001000000000004", "2026-11-24T23:59:59Z", "Old information", "")
	current := news(fixtureDiscordMessage, "2026-11-26T12:59:00Z", "<@1173001000000000010> Fed releases minutes\nhttps://example.org/path?private=token", `,"author":{"username":"external alias"}`)
	embed := news("1173001000000000005", "2026-11-25T10:00:00-05:00", "https://example.org/article", `,"embeds":[{"title":"Inflation report scheduled","url":"https://example.org/hidden"}]`)
	future := news("1173001000000000006", "2026-11-26T13:00:01Z", "Future information", "")
	msgs := "[" + strings.Join([]string{old, embed, current, current, future}, ",") + "]"
	got := []string{}
	get := func(_ context.Context, requestURL, auth string, destination any) error {
		got = append(got, requestURL)
		if auth != "Bot fixture" {
			t.Fatal("missing supported bot authorization")
		}
		switch requestURL {
		case endpoint:
			return json.Unmarshal([]byte(fmt.Sprintf(`{"id":%q,"guild_id":%q,"type":5}`, fixtureDiscordChannel, fixtureDiscordGuild)), destination)
		case endpoint + "/messages?limit=20":
			return json.Unmarshal([]byte(msgs), destination)
		default:
			t.Fatalf("unexpected read %s", requestURL)
			return nil
		}
	}
	target := discordLinks + fixtureDiscordGuild + "/" + fixtureDiscordChannel
	items, err := fetchDiscord(context.Background(), get, Settings{Target: target, Token: "Bot fixture"}, cutoff)
	if err != nil || len(items) != 2 || len(got) != 2 {
		t.Fatalf("wanted two results from two reads; got %#v, %v and %d calls", items, err, len(got))
	}
	if items[0].Text != "Fed releases minutes" || items[0].URL != target+"/"+fixtureDiscordMessage || items[0].PublishedAt.Location() != time.UTC ||
		items[1].Text != "Inflation report scheduled" {
		t.Fatalf("unexpected extracted evidence: %#v", items)
	}
}

func TestDiscordRefusesUnverifiedGuildOrDirectMessage(t *testing.T) {
	for _, fixture := range []string{
		`{"id":"1173001000000000001","guild_id":"1173001000000000188","type":0}`,
		`{"id":"1173001000000000001","type":1}`,
		`{"id":"1173001000000000001","guild_id":"1173001000000000002"}`,
	} {
		calls := 0
		get := func(_ context.Context, _, _ string, destination any) error {
			calls++
			return json.Unmarshal([]byte(fixture), destination)
		}
		target := discordLinks + fixtureDiscordGuild + "/" + fixtureDiscordChannel
		_, err := fetchDiscord(context.Background(), get, Settings{Target: target, Token: "fixture"}, time.Now())
		if err == nil || calls != 1 {
			t.Fatalf("unverified channel read: %v, calls=%d", err, calls)
		}
	}
}

func TestDiscordReportsHiddenMessageContent(t *testing.T) {
	endpoint := discordAPI + "/channels/" + fixtureDiscordChannel
	get := func(_ context.Context, requestURL, _ string, destination any) error {
		if requestURL == endpoint {
			return json.Unmarshal([]byte(fmt.Sprintf(`{"id":%q,"guild_id":%q,"type":0}`, fixtureDiscordChannel, fixtureDiscordGuild)), destination)
		}
		return json.Unmarshal([]byte(fmt.Sprintf(`[{"id":%q,"channel_id":%q,"timestamp":"2026-11-26T13:00:00Z","content":"","embeds":[]}]`, fixtureDiscordMessage, fixtureDiscordChannel)), destination)
	}
	_, err := fetchDiscord(context.Background(), get, Settings{Target: fixtureDiscordChannel, Token: "fixture"}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "Message Content Intent") {
		t.Fatalf("expected actionable permission error, got %v", err)
	}
}
