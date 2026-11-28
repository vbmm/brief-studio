package engine

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"time"
	_ "time/tzdata" // Keep New York session calculations available without system zoneinfo.
)

const (
	quoteAgeLimit      = 45 * time.Minute
	recapQuoteAgeLimit = 7 * 24 * time.Hour
)

var newYork = func() *time.Location {
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(err)
	}
	return zone
}()

type instrument struct {
	symbol string
	name   string
}

var nq = instrument{symbol: "NQ", name: "Nasdaq 100"}
var es = instrument{symbol: "ES", name: "S&P 500"}

func (i instrument) source() Source {
	return Source{Label: "Yahoo Finance · " + i.symbol + "=F (CME, différé)",
		URL: "https://finance.yahoo.com/quote/" + i.symbol + "%3DF/"}
}

type quote struct {
	price float64
	time  time.Time
}

type session struct {
	day              string
	low, high, close float64
}

type observation struct {
	instrument
	quote         *quote
	lastSeenQuote *quote
	session       *session
	message       string
}

func fetchObservation(ctx context.Context, load fetchFunc, future instrument, cutoff time.Time) observation {
	endpoint := "https://query1.finance.yahoo.com/v8/finance/chart/" + future.symbol + "%3DF?range=5d&interval=5m"
	body, err := load(ctx, endpoint, "application/json", 2_000_000)
	if err != nil {
		return observation{instrument: future, message: "La série Yahoo Finance est temporairement indisponible."}
	}
	return decodeObservation(body, future, cutoff)
}

type chartSeries struct {
	Meta struct {
		Symbol             string  `json:"symbol"`
		InstrumentType     string  `json:"instrumentType"`
		Currency           string  `json:"currency"`
		DataGranularity    string  `json:"dataGranularity"`
		RegularMarketPrice float64 `json:"regularMarketPrice"`
		RegularMarketTime  int64   `json:"regularMarketTime"`
	} `json:"meta"`
	Timestamps []int64 `json:"timestamp"`
	Indicators struct {
		Quote []barColumns `json:"quote"`
	} `json:"indicators"`
}

type barColumns struct {
	Open  []float64 `json:"open"`
	Low   []float64 `json:"low"`
	High  []float64 `json:"high"`
	Close []float64 `json:"close"`
}

func decodeObservation(body []byte, future instrument, cutoff time.Time) observation {
	result := observation{instrument: future}
	var envelope struct {
		Chart struct {
			Result []chartSeries `json:"result"`
		} `json:"chart"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || len(envelope.Chart.Result) == 0 {
		result.message = "La série Yahoo Finance est temporairement indisponible."
		return result
	}
	series := envelope.Chart.Result[0]
	m := series.Meta
	if m.Symbol != future.symbol+"=F" || m.InstrumentType != "FUTURE" || m.Currency != "USD" || m.DataGranularity != "5m" {
		result.message = "La réponse ne correspond pas au contrat attendu."
		return result
	}
	result.session = cashSession(series, cutoff)
	if m.RegularMarketTime <= 0 || !validPrice(m.RegularMarketPrice) {
		result.message = "Dernière cotation horodatée indisponible."
		return result
	}
	observed := time.Unix(m.RegularMarketTime, 0)
	age := cutoff.Sub(observed)
	switch {
	case age < 0:
		result.message = "Horodatage de cotation incohérent."
	case age > quoteAgeLimit:
		result.message = "Dernière cotation trop ancienne (" + localDate(observed) + " ET)."
		if age <= recapQuoteAgeLimit {
			result.lastSeenQuote = &quote{price: m.RegularMarketPrice, time: observed}
		}
	default:
		result.quote = &quote{price: m.RegularMarketPrice, time: observed}
	}
	return result
}

func cashSession(series chartSeries, now time.Time) *session {
	open, close, ok := lastCompletedCash(now)
	if !ok || len(series.Indicators.Quote) == 0 {
		return nil
	}
	columns := series.Indicators.Quote[0]
	count := len(series.Timestamps)
	if len(columns.Open) != count || len(columns.Low) != count || len(columns.High) != count || len(columns.Close) != count {
		return nil
	}
	const barSeconds = 300
	start, end := open.Unix(), close.Unix()
	indices := make([]int, 0, (end-start)/barSeconds)
	for index, stamp := range series.Timestamps {
		if start <= stamp && stamp < end {
			indices = append(indices, index)
		}
	}
	if int64(len(indices)) != (end-start)/barSeconds {
		return nil
	}
	sort.Slice(indices, func(a, b int) bool { return series.Timestamps[indices[a]] < series.Timestamps[indices[b]] })
	result := session{day: open.Format("2006-01-02"), low: math.Inf(1)}
	for step, index := range indices {
		if series.Timestamps[index] != start+int64(step)*barSeconds {
			return nil
		}
		o, l, h, c := columns.Open[index], columns.Low[index], columns.High[index], columns.Close[index]
		if !validPrice(o) || !validPrice(l) || !validPrice(h) || !validPrice(c) || l > math.Min(o, c) || h < math.Max(o, c) {
			return nil
		}
		result.low = math.Min(result.low, l)
		result.high = math.Max(result.high, h)
		result.close = c
	}
	return &result
}

func validPrice(value float64) bool {
	return value > 0 && !math.IsInf(value, 0) && !math.IsNaN(value)
}

// https://www.nyse.com/trade/hours-calendars. Unknown years omit the RTH reference.
var cashHolidays = map[string]bool{
	"2026-01-01": true, "2026-01-19": true, "2026-02-16": true, "2026-04-03": true, "2026-05-25": true,
	"2026-06-19": true, "2026-07-03": true, "2026-09-07": true, "2026-11-26": true, "2026-12-25": true,
	"2027-01-01": true, "2027-01-18": true, "2027-02-15": true, "2027-03-26": true, "2027-05-31": true,
	"2027-06-18": true, "2027-07-05": true, "2027-09-06": true, "2027-11-25": true, "2027-12-24": true,
	"2028-01-17": true, "2028-02-21": true, "2028-04-14": true, "2028-05-29": true, "2028-06-19": true,
	"2028-07-04": true, "2028-09-04": true, "2028-11-23": true, "2028-12-25": true,
}

var earlyCashCloses = map[string]bool{
	"2026-11-27": true, "2026-12-24": true, "2027-11-26": true, "2028-07-03": true, "2028-11-24": true,
}

func lastCompletedCash(now time.Time) (time.Time, time.Time, bool) {
	local := now.In(newYork)
	for offset := 0; offset < 8; offset++ {
		day := local.AddDate(0, 0, -offset)
		if day.Year() < 2026 || day.Year() > 2028 {
			break
		}
		date := day.Format("2006-01-02")
		if day.Weekday() == time.Sunday || day.Weekday() == time.Saturday || cashHolidays[date] {
			continue
		}
		closeHour := 16
		if earlyCashCloses[date] {
			closeHour = 13
		}
		close := time.Date(day.Year(), day.Month(), day.Day(), closeHour, 0, 0, 0, newYork)
		if !close.After(now) {
			return time.Date(day.Year(), day.Month(), day.Day(), 9, 30, 0, 0, newYork), close, true
		}
	}
	return time.Time{}, time.Time{}, false
}

func localDate(date time.Time) string { return date.In(newYork).Format("2006-01-02 15:04") }
