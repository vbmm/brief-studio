package engine

import (
	"strings"
	"testing"

	"briefstudio/web/internal/sources"
)

func TestOptionalSourcePrivacy(t *testing.T) {
	items := []NewsFeed{
		{Provider: sources.Discord, Items: []sources.Headline{{Text: "extrait Test Discord", URL: "https://discord.com/channels/10/11/12"}}},
		{Provider: sources.X, Items: []sources.Headline{{Text: "contenu Test X", URL: "https://x.com/deitaone/status/4"}}},
	}
	prompt := newsPrompt(items)
	if !strings.Contains(prompt, "extrait Test Discord") || strings.Contains(prompt, "contenu Test X") || strings.Contains(prompt, "discord.com") {
		t.Fatal("prompt is missing context or includes restricted source data")
	}
	var markdown strings.Builder
	var document Document
	writeNews(&markdown, &document, items)
	if len(document.Sources) != 2 || document.Sources[1].URL != items[1].Items[0].URL ||
		strings.Contains(markdown.String(), "extrait Test Discord") || strings.Contains(markdown.String(), "contenu Test X") {
		t.Fatal("the archive should retain source links only")
	}
}
