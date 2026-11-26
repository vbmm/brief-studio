package sources

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

const (
	publicGEXBaseURL = "https://lab.flashalpha.com/v1/stock/"
	publicGEXSource  = "FlashAlpha · options sur futures CME (différé)"
	publicGEXURL     = "https://flashalpha.com/methodology/futures"
	publicGEXLimit   = 128 << 10
)

var (
	ErrPublicGEXUnavailable = errors.New("FlashAlpha ne fournit aucune donnée gamma récente et vérifiable pour NQ ou ES.")
	errPublicGEXConnection  = errors.New("La source gamma publique FlashAlpha est inaccessible pour le moment.")
	publicGEXClient         = &http.Client{
		Timeout: 8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
)

type publicGEXSummary struct {
	Symbol   string     `json:"symbol"`
	AsOf     *time.Time `json:"as_of"`
	DataAsOf struct {
		Futures        time.Time `json:"futures_feed"`
		FuturesOptions time.Time `json:"futures_options_feed"`
	} `json:"data_as_of"`
	SnapshotAsOf struct {
		CapturedAt time.Time `json:"captured_at"`
		Delayed    bool      `json:"delayed"`
	} `json:"snapshot_as_of"`
	Exposure struct {
		Flip       *float64 `json:"gamma_flip"`
		FlipStatus string   `json:"gamma_flip_status"`
		CallWall   *float64 `json:"call_wall"`
		PutWall    *float64 `json:"put_wall"`
		Regime     string   `json:"regime"`
	} `json:"exposure"`
}

// FetchPublicGEX reads delayed CME futures options data at the requested cutoff.
func FetchPublicGEX(ctx context.Context, cutoff time.Time) (*MarketSnapshot, error) {
	return fetchPublicGEX(ctx, cutoff, publicGEXClient)
}

func fetchPublicGEX(ctx context.Context, cutoff time.Time, client *http.Client) (*MarketSnapshot, error) {
	type result struct {
		name       string
		instrument MarketInstrument
		observedAt time.Time
		err        error
	}
	results := make(chan result, 2)
	for _, name := range []string{"NQ", "ES"} {
		go func() {
			instrument, observedAt, err := readPublicGEX(ctx, client, name, cutoff)
			results <- result{name, instrument, observedAt, err}
		}()
	}

	snapshot := &MarketSnapshot{
		Schema:      MarketSnapshotSchema,
		Source:      publicGEXSource,
		SourceURL:   publicGEXURL,
		Instruments: make(map[string]MarketInstrument, 2),
	}
	connectionsFailed := 0
	for range 2 {
		item := <-results
		if item.err == errPublicGEXConnection {
			connectionsFailed++
		}
		if item.err != nil {
			continue
		}
		snapshot.Instruments[item.name] = item.instrument
		if snapshot.ObservedAt.IsZero() || item.observedAt.Before(snapshot.ObservedAt) {
			snapshot.ObservedAt = item.observedAt
		}
	}
	if connectionsFailed == 2 {
		return nil, errPublicGEXConnection
	}
	if len(snapshot.Instruments) == 0 {
		return nil, ErrPublicGEXUnavailable
	}
	return snapshot, nil
}

func readPublicGEX(ctx context.Context, client *http.Client, name string, cutoff time.Time) (MarketInstrument, time.Time, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, publicGEXBaseURL+name+"%3DF/summary", nil)
	if err != nil {
		return MarketInstrument{}, time.Time{}, errPublicGEXConnection
	}
	req.Header.Set("Accept", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return MarketInstrument{}, time.Time{}, errPublicGEXConnection
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return MarketInstrument{}, time.Time{}, ErrPublicGEXUnavailable
	}
	if response.StatusCode != http.StatusOK {
		return MarketInstrument{}, time.Time{}, errPublicGEXConnection
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, publicGEXLimit+1))
	if err != nil {
		return MarketInstrument{}, time.Time{}, errPublicGEXConnection
	}
	var summary publicGEXSummary
	if len(body) > publicGEXLimit || json.Unmarshal(body, &summary) != nil ||
		summary.Symbol != name+"=F" || !summary.SnapshotAsOf.Delayed {
		return MarketInstrument{}, time.Time{}, ErrPublicGEXUnavailable
	}

	observedAt := summary.observedAt(cutoff)
	gamma := summary.gamma()
	if observedAt.IsZero() || !validGamma(gamma) {
		return MarketInstrument{}, time.Time{}, ErrPublicGEXUnavailable
	}
	return MarketInstrument{Symbol: summary.Symbol, Gamma: gamma}, observedAt, nil
}

func (s publicGEXSummary) observedAt(cutoff time.Time) time.Time {
	timestamps := []time.Time{s.SnapshotAsOf.CapturedAt, s.DataAsOf.Futures, s.DataAsOf.FuturesOptions}
	if s.AsOf != nil {
		timestamps = append(timestamps, *s.AsOf)
	}
	oldest := cutoff
	for _, timestamp := range timestamps {
		if !(MarketSnapshot{ObservedAt: timestamp}).Fresh(cutoff) {
			return time.Time{}
		}
		if timestamp.Before(oldest) {
			oldest = timestamp
		}
	}
	return oldest.UTC()
}

func (s publicGEXSummary) gamma() *GammaInput {
	gamma := &GammaInput{}
	if validMarketPrices(s.Exposure.CallWall) {
		gamma.CallWall = s.Exposure.CallWall
	}
	if validMarketPrices(s.Exposure.PutWall) {
		gamma.PutWall = s.Exposure.PutWall
	}
	if s.Exposure.FlipStatus == "available" && s.Exposure.Flip != nil && validMarketPrices(s.Exposure.Flip) {
		gamma.Flip = s.Exposure.Flip
		switch s.Exposure.Regime {
		case "positive_gamma":
			gamma.Regime = "positive"
		case "negative_gamma":
			gamma.Regime = "negative"
		}
	}
	return gamma
}
