package sources

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	MarketSnapshotSchema = "brief-studio/market-input/v1"
	MaxMarketSnapshotAge = 24 * time.Hour
	maxMarketPrice       = 10_000_000
)

// MarketSnapshot is a user-supplied observation in native futures price units.
type MarketSnapshot struct {
	Schema      string                      `json:"schema"`
	ObservedAt  time.Time                   `json:"observedAt"`
	Source      string                      `json:"source"`
	SourceURL   string                      `json:"sourceURL,omitempty"`
	Instruments map[string]MarketInstrument `json:"instruments"`
}

type MarketInstrument struct {
	Symbol string        `json:"symbol"`
	Gamma  *GammaInput   `json:"gamma,omitempty"`
	Levels *MarketLevels `json:"levels,omitempty"`
}

type GammaInput struct {
	Regime   string   `json:"regime,omitempty"`
	Flip     *float64 `json:"flip,omitempty"`
	CallWall *float64 `json:"callWall,omitempty"`
	PutWall  *float64 `json:"putWall,omitempty"`
}

type MarketLevels struct {
	POC  *float64 `json:"poc,omitempty"`
	VAH  *float64 `json:"vah,omitempty"`
	VAL  *float64 `json:"val,omitempty"`
	VWAP *float64 `json:"vwap,omitempty"`
}

// Validate checks provenance and structure. Use Fresh to decide whether a
// valid observation is recent enough at the particular brief's cutoff.
func (s MarketSnapshot) Validate(now time.Time) error {
	if s.Schema != MarketSnapshotSchema {
		return errors.New("Format de données de marché inconnu.")
	}
	if now.IsZero() || s.ObservedAt.IsZero() || s.ObservedAt.After(now) {
		return errors.New("L’heure d’observation des données de marché est manquante ou future.")
	}
	if !validMarketSource(s.Source) {
		return errors.New("Indique un nom de source sur une seule ligne, de 100 caractères au maximum.")
	}
	if s.SourceURL != "" && !validMarketSourceURL(s.SourceURL) {
		return errors.New("Le lien de la source doit être une adresse HTTPS sans identifiants.")
	}
	if len(s.Instruments) < 1 || len(s.Instruments) > 2 {
		return errors.New("Ajoute un ou deux instruments parmi NQ et ES.")
	}
	for name, instrument := range s.Instruments {
		if (name != "NQ" && name != "ES") || instrument.Symbol != name+"=F" {
			return errors.New("Utilise NQ avec le symbole NQ=F ou ES avec le symbole ES=F.")
		}
		if instrument.Gamma == nil && instrument.Levels == nil {
			return fmt.Errorf("Ajoute des données gamma ou des niveaux pour %s.", name)
		}
		if g := instrument.Gamma; g != nil && !validGamma(g) {
			return fmt.Errorf("Les données gamma de %s sont vides ou invalides.", name)
		}
		if p := instrument.Levels; p != nil && !validLevels(p) {
			return fmt.Errorf("Les niveaux de %s sont vides ou invalides. Respecte VAL ≤ POC ≤ VAH pour les valeurs présentes.", name)
		}
	}
	return nil
}

// Fresh uses an inclusive rolling age limit and rejects future observations.
func (s MarketSnapshot) Fresh(cutoff time.Time) bool {
	return !cutoff.IsZero() && !s.ObservedAt.IsZero() && !s.ObservedAt.After(cutoff) &&
		!s.ObservedAt.Before(cutoff.Add(-MaxMarketSnapshotAge))
}

func validMarketSource(label string) bool {
	return label != "" && utf8.ValidString(label) && utf8.RuneCountInString(label) <= 100 &&
		label == strings.TrimSpace(label) && !strings.ContainsAny(label, "<>[]*`\\") &&
		!strings.ContainsFunc(label, func(r rune) bool {
			return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp)
		})
}

func validMarketSourceURL(value string) bool {
	if len(value) > 2048 || strings.ContainsFunc(value, unicode.IsSpace) {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Opaque != "" {
		return false
	}
	if !safeCitationParameters(parsed.RawQuery) {
		return false
	}
	return !strings.Contains(parsed.Fragment, "=") || safeCitationParameters(parsed.Fragment)
}

func safeCitationParameters(raw string) bool {
	params, err := url.ParseQuery(raw)
	if err != nil {
		return false
	}
	compact := strings.NewReplacer("_", "", "-", "")
	for key := range params {
		key = compact.Replace(strings.ToLower(key))
		if key == "key" || key == "auth" || key == "sig" {
			return false
		}
		for _, part := range []string{"token", "secret", "password", "credential", "apikey", "authorization", "signature"} {
			if strings.Contains(key, part) {
				return false
			}
		}
	}
	return true
}

func validGamma(g *GammaInput) bool {
	switch g.Regime {
	case "", "positive", "negative", "neutral":
	default:
		return false
	}
	return (g.Regime != "" || g.Flip != nil || g.CallWall != nil || g.PutWall != nil) &&
		validMarketPrices(g.Flip, g.CallWall, g.PutWall)
}

func validLevels(p *MarketLevels) bool {
	return (p.POC != nil || p.VAH != nil || p.VAL != nil || p.VWAP != nil) &&
		validMarketPrices(p.POC, p.VAH, p.VAL, p.VWAP) &&
		inOrder(p.VAL, p.POC) && inOrder(p.POC, p.VAH) && inOrder(p.VAL, p.VAH)
}

func validMarketPrices(prices ...*float64) bool {
	for _, price := range prices {
		if price != nil && !(*price > 0 && *price <= maxMarketPrice) {
			return false
		}
	}
	return true
}

func inOrder(low, high *float64) bool {
	return low == nil || high == nil || *low <= *high
}
