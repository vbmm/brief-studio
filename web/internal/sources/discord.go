package sources

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	discordAPI           = "https://discord.com/api/v10"
	discordLinks         = "https://discord.com/channels/"
	discordMessageLimit  = 20
	discordLookback      = 36 * time.Hour
	discordExcerptLength = 320
)

var (
	errDiscordChannel    = errors.New("Colle l’identifiant ou le lien Discord d’un salon de serveur.")
	errDiscordToken      = errors.New("Renseigne le jeton d’un bot Discord autorisé à lire ce salon.")
	discordMessageMarkup = regexp.MustCompile(`(?i)https?://[^\s<>\])]+|<[@#][!&]?[0-9]+>|@\b(?:everyone|here)\b`)
)

type discordChannel struct {
	ID      string `json:"id"`
	GuildID string `json:"guild_id"`
	Type    *int   `json:"type"`
}

type discordMessage struct {
	ID        string `json:"id"`
	ChannelID string `json:"channel_id"`
	Timestamp string `json:"timestamp"`
	Content   string `json:"content"`
	Embeds    []struct {
		Title       string `json:"title"`
		Description string `json:"description"`
	} `json:"embeds"`
}

func normalizeDiscordTarget(input string) (string, error) {
	channel, guild, err := parseDiscordTarget(input)
	if err != nil || guild == "" {
		return channel, err
	}
	return discordLinks + guild + "/" + channel, nil
}

func parseDiscordTarget(input string) (channel, guild string, err error) {
	input = strings.TrimSpace(input)
	if discordSnowflake(input) {
		return input, "", nil
	}
	link, err := url.Parse(input)
	if err != nil || link.Scheme != "https" || !strings.EqualFold(link.Host, "discord.com") ||
		link.User != nil || link.RawQuery != "" || link.ForceQuery || link.Fragment != "" || link.EscapedPath() != link.Path {
		return "", "", errDiscordChannel
	}
	parts := strings.Split(strings.TrimSuffix(link.Path, "/"), "/")
	if len(parts) < 4 || len(parts) > 5 || parts[0] != "" || parts[1] != "channels" ||
		!discordSnowflake(parts[2]) || !discordSnowflake(parts[3]) || (len(parts) == 5 && !discordSnowflake(parts[4])) {
		return "", "", errDiscordChannel
	}
	return parts[3], parts[2], nil
}

func discordSnowflake(value string) bool {
	if len(value) < 17 || len(value) > 20 {
		return false
	}
	id, err := strconv.ParseUint(value, 10, 64)
	return err == nil && id > 0 && strconv.FormatUint(id, 10) == value
}

func fetchDiscord(ctx context.Context, get getter, settings Settings, cutoff time.Time) ([]Headline, error) {
	channelID, guildID, err := parseDiscordTarget(settings.Target)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(settings.Token), "Bot "))
	if token == "" || token == "Bot" || strings.ContainsFunc(token, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return nil, errDiscordToken
	}
	authorization := "Bot " + token
	endpoint := discordAPI + "/channels/" + channelID
	var channel discordChannel
	if err := get(ctx, endpoint, authorization, &channel); err != nil {
		return nil, err
	}
	if channel.ID != channelID || !discordSnowflake(channel.GuildID) || (guildID != "" && guildID != channel.GuildID) ||
		channel.Type == nil || (*channel.Type != 0 && *channel.Type != 5) {
		return nil, errors.New("Discord requiert un salon textuel ou d’annonces du serveur indiqué.")
	}
	var messages []discordMessage
	if err := get(ctx, endpoint+"/messages?limit="+strconv.Itoa(discordMessageLimit), authorization, &messages); err != nil {
		return nil, err
	}
	if messages == nil || len(messages) > discordMessageLimit {
		return nil, errors.New("Discord a renvoyé une liste de messages invalide.")
	}

	results := make([]Headline, 0, len(messages))
	seen := make(map[string]bool, len(messages))
	hasReadableText := false
	for _, message := range messages {
		if message.ChannelID != channel.ID || !discordSnowflake(message.ID) {
			return nil, errors.New("Discord a renvoyé un message incompatible avec le salon configuré.")
		}
		text, readable := discordExcerpt(message)
		hasReadableText = hasReadableText || readable
		at, err := time.Parse(time.RFC3339Nano, message.Timestamp)
		if err != nil || text == "" || seen[message.ID] || at.After(cutoff) || at.Before(cutoff.Add(-discordLookback)) {
			continue
		}
		seen[message.ID] = true
		permalink := discordLinks + channel.GuildID + "/" + channel.ID + "/" + message.ID
		results = append(results, Headline{Text: text, URL: permalink, PublishedAt: at.UTC()})
	}
	if len(messages) > 0 && !hasReadableText {
		return nil, errors.New("Les messages Discord sont sans texte lisible. Vérifie l’accès à l’historique et l’option Message Content Intent du bot.")
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].PublishedAt.After(results[j].PublishedAt) })
	return results, nil
}

func discordExcerpt(message discordMessage) (text string, readable bool) {
	candidates := []string{message.Content}
	for _, embed := range message.Embeds {
		candidates = append(candidates, embed.Title, embed.Description)
	}
	for _, candidate := range candidates {
		readable = readable || strings.TrimSpace(candidate) != ""
		text = discordMessageMarkup.ReplaceAllString(candidate, " ")
		text = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
				return ' '
			}
			return r
		}, text)
		text = strings.Join(strings.Fields(text), " ")
		if text == "" {
			continue
		}
		runes := []rune(text)
		if len(runes) > discordExcerptLength {
			return strings.TrimSpace(string(runes[:discordExcerptLength])), readable
		}
		return text, readable
	}
	return "", readable
}
