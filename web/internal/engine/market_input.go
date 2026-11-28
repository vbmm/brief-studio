package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"briefstudio/web/internal/sources"
)

type GEXMode string

const (
	WithoutGEX GEXMode = "without"
	WithGEX    GEXMode = "with"
	PublicGEX  GEXMode = "public"
)

var ErrGEXUnavailable = errors.New("Aucun GEX importé récent. Importe un relevé de moins de 24 h ou choisis Sans GEX.")

func (m GEXMode) Valid() bool { return m == "" || m == WithoutGEX || m == WithGEX || m == PublicGEX }

type marketInput struct {
	mode        GEXMode
	source      string
	sourceURL   string
	observedAt  time.Time
	instruments map[string]sources.MarketInstrument
	stale       bool
	unreadable  bool
}

func selectMarketInput(mode GEXMode, snapshot *sources.MarketSnapshot, cutoff time.Time) (marketInput, error) {
	result := marketInput{mode: mode}
	if !mode.Valid() {
		return result, errors.New("Mode GEX inconnu.")
	}
	hasGamma := false
	if snapshot != nil {
		if err := snapshot.Validate(cutoff); err != nil {
			return result, err
		}
		result.stale = !snapshot.Fresh(cutoff)
		if !result.stale {
			result.source, result.sourceURL, result.observedAt = snapshot.Source, snapshot.SourceURL, snapshot.ObservedAt
			result.instruments = make(map[string]sources.MarketInstrument, len(snapshot.Instruments))
			for symbol, input := range snapshot.Instruments {
				if mode != WithGEX {
					input.Gamma = nil
				}
				hasGamma = hasGamma || input.Gamma != nil
				if input.Gamma != nil || input.Levels != nil {
					result.instruments[symbol] = input
				}
			}
		}
	}
	if mode == WithGEX && !hasGamma {
		return result, ErrGEXUnavailable
	}
	return result, nil
}

func (m marketInput) prompt() string {
	if len(m.instruments) == 0 {
		return "\nCe brief exclut le GEX et ne comporte pas de niveaux importés."
	}
	mode := "Le GEX est exclu de ce brief."
	if m.mode == WithGEX {
		mode = "Le GEX importé est une estimation. Son signe ne donne aucune direction."
	}
	data, _ := json.Marshal(m.instruments)
	return "\n" + mode + " Les repères importés sont exprimés en points du future indiqué. " +
		"Utilise chaque relevé uniquement pour son propre contrat. Ne déduis aucun niveau absent. " +
		"Les repères servent de contexte à surveiller et ne valident aucune hypothèse directionnelle. " +
		"Relevé en UTC au " + iso(m.observedAt) + " comme objet JSON : " + string(data)
}

func (m marketInput) modeLabel() string {
	if m.mode == WithGEX {
		return "Avec GEX importé"
	}
	if m.mode == PublicGEX {
		return "Sans GEX · panneau public facultatif"
	}
	return "Sans GEX"
}

type importedLevel struct {
	label string
	value *float64
}

func levelLabels(levels ...importedLevel) []string {
	var entries []string
	for _, level := range levels {
		if level.value != nil {
			entries = append(entries, level.label+" "+formatPrice(*level.value))
		}
	}
	return entries
}

func gammaLabel(gamma *sources.GammaInput) string {
	entries := levelLabels(importedLevel{"bascule gamma", gamma.Flip}, importedLevel{"call wall", gamma.CallWall},
		importedLevel{"put wall", gamma.PutWall})
	if regime := map[string]string{"positive": "positif", "negative": "négatif", "neutral": "neutre"}[gamma.Regime]; regime != "" {
		entries = append([]string{"régime " + regime}, entries...)
	}
	return strings.Join(entries, " · ")
}

func (m marketInput) writeInstrument(draft *strings.Builder, symbol string) {
	input := m.instruments[symbol]
	if input.Gamma != nil {
		fmt.Fprintf(draft, "- **GEX importé** %s.\n", gammaLabel(input.Gamma))
	} else if m.mode == WithGEX {
		draft.WriteString("- **GEX importé** Indisponible pour ce contrat.\n")
	}
	if levels := input.Levels; levels != nil {
		entries := levelLabels(importedLevel{"VAL", levels.VAL}, importedLevel{"POC", levels.POC},
			importedLevel{"VAH", levels.VAH}, importedLevel{"VWAP", levels.VWAP})
		fmt.Fprintf(draft, "- **Niveaux importés** %s.\n", strings.Join(entries, " · "))
	}
	if input.Gamma != nil || input.Levels != nil {
		fmt.Fprintf(draft, "- **Source du relevé** %s · %s ET · %s · points du future.\n",
			m.source, localDate(m.observedAt), input.Symbol)
	}
}

// FlashAlpha's public numbers are available for local use. Keep the rendered
// block separate from the prompt and the document text used in the HTML export.
func publicGEXReadout(snapshot *sources.MarketSnapshot, cutoff time.Time) (string, error) {
	if snapshot == nil || snapshot.Validate(cutoff) != nil || !snapshot.Fresh(cutoff) ||
		snapshot.Instruments["NQ"].Gamma == nil && snapshot.Instruments["ES"].Gamma == nil {
		return "", sources.ErrPublicGEXUnavailable
	}
	var draft strings.Builder
	fmt.Fprintf(&draft, "## GEX public différé\n\n%s · relevé au %s ET. Niveaux en points du future.\n\n",
		snapshot.Source, localDate(snapshot.ObservedAt))
	for _, symbol := range []string{"NQ", "ES"} {
		input := snapshot.Instruments[symbol]
		value := "Aucun relevé récent."
		if input.Gamma != nil {
			value = gammaLabel(input.Gamma) + "."
		}
		fmt.Fprintf(&draft, "- **%s** %s\n", symbol, value)
	}
	if snapshot.SourceURL != "" {
		fmt.Fprintf(&draft, "\nMéthodologie %s\n", snapshot.SourceURL)
	}
	return draft.String(), nil
}

func (m marketInput) writeLimits(draft *strings.Builder, doc *Document) {
	if m.unreadable {
		draft.WriteString("- Le relevé de marché importé était illisible. Ses données ont été écartées.\n")
	}
	if m.stale {
		draft.WriteString("- Le relevé de marché importé a plus de 24 heures. Ses données ont été écartées.\n")
	}
	if len(m.instruments) != 0 {
		draft.WriteString("- Les repères importés sont fournis par l’outil cité.\n")
		if m.sourceURL != "" {
			doc.Sources = append(doc.Sources, Source{Label: "Relevé de marché · " + m.source, URL: m.sourceURL})
		}
	}
}
