// Package sources reads the optional user-configured news feeds.
package sources

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Provider string

const (
	Discord Provider = "discord"
	X       Provider = "x"
)

func (provider Provider) Label() string {
	if provider == Discord {
		return "Discord"
	}
	return "X"
}

type Settings struct {
	Target string
	Token  string
}

type Headline struct {
	Text        string
	URL         string
	PublishedAt time.Time
}

type getter func(context.Context, string, string, any) error

type sourceError struct {
	status  int
	message string
}

func (err sourceError) Error() string { return err.message }

func Status(err error) int {
	var denied sourceError
	if errors.As(err, &denied) {
		return denied.status
	}
	return http.StatusBadGateway
}

func Normalize(provider Provider, settings Settings) (Settings, error) {
	var err error
	switch provider {
	case Discord:
		settings.Target, err = normalizeDiscordTarget(settings.Target)
		settings.Token = strings.TrimPrefix(strings.TrimSpace(settings.Token), "Bot ")
	case X:
		settings.Target, err = normalizeXTarget(settings.Target)
		settings.Token = strings.TrimPrefix(strings.TrimSpace(settings.Token), "Bearer ")
	default:
		return Settings{}, errors.New("Source inconnue.")
	}
	if err != nil {
		return Settings{}, err
	}
	if settings.Token == "" || len(settings.Token) > 4096 || strings.ContainsFunc(settings.Token, func(r rune) bool {
		return r < '!' || r > '~'
	}) {
		return Settings{}, errors.New("Saisis un jeton d’application valide.")
	}
	return settings, nil
}

type Client struct{ http *http.Client }

func New() *Client {
	return &Client{http: &http.Client{Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (client *Client) Fetch(ctx context.Context, provider Provider, settings Settings, cutoff time.Time) ([]Headline, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	switch provider {
	case Discord:
		return fetchDiscord(ctx, client.get, settings, cutoff)
	case X:
		return fetchX(ctx, client.get, settings, cutoff)
	default:
		return nil, errors.New("Source inconnue.")
	}
}

func (client *Client) get(ctx context.Context, endpoint, auth string, target any) error {
	dest, err := url.Parse(endpoint)
	if err != nil || dest.Scheme != "https" || dest.User != nil || !((dest.Host == "discord.com" && strings.HasPrefix(auth, "Bot ")) ||
		(dest.Host == "api.x.com" && strings.HasPrefix(auth, "Bearer "))) {
		return sourceError{http.StatusBadGateway, "Adresse du fournisseur invalide."}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return sourceError{http.StatusBadGateway, "Requête du fournisseur invalide."}
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/json")
	response, err := client.http.Do(req)
	if err != nil {
		var networkError interface{ Timeout() bool }
		if errors.As(err, &networkError) && networkError.Timeout() {
			return sourceError{http.StatusGatewayTimeout, "Le fournisseur n’a pas répondu à temps."}
		}
		return sourceError{http.StatusBadGateway, "Impossible de joindre le fournisseur."}
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return sourceError{http.StatusUnauthorized, "Le fournisseur refuse ce jeton."}
	case http.StatusPaymentRequired:
		return sourceError{http.StatusPaymentRequired, "Le fournisseur exige des crédits pour cette requête."}
	case http.StatusForbidden, http.StatusNotFound:
		return sourceError{http.StatusForbidden, "Ce jeton n’a pas accès à la source demandée."}
	case http.StatusTooManyRequests:
		return sourceError{http.StatusTooManyRequests, "La limite de requêtes du fournisseur est atteinte."}
	default:
		return sourceError{http.StatusBadGateway, "Le fournisseur n’a pas fourni de données utilisables."}
	}
	const limit = 1 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || len(body) > limit || json.Unmarshal(body, target) != nil {
		return sourceError{http.StatusBadGateway, "La réponse du fournisseur est illisible."}
	}
	return nil
}
