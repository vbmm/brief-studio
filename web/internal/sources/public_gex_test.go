package sources

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type publicGEXTransport func(*http.Request) (*http.Response, error)

func (f publicGEXTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func publicGEXPayload(symbol string, captured, underlying, options time.Time, exposure, extra string) string {
	return fmt.Sprintf(`{
		"symbol": %q,
		"snapshot_as_of": {"captured_at": %q, "delayed": true},
		"data_as_of": {"futures_feed": %q, "futures_options_feed": %q},
		"exposure": %s%s
	}`, symbol, captured.Format(time.RFC3339Nano), underlying.Format(time.RFC3339Nano), options.Format(time.RFC3339Nano), exposure, extra)
}

func mockPublicGEXClient(t *testing.T, payloads map[string]string) *http.Client {
	t.Helper()
	client := *publicGEXClient
	var calls atomic.Int32
	client.Transport = publicGEXTransport(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		if req.URL.Scheme != "https" || req.URL.Host != "lab.flashalpha.com" || req.URL.RawQuery != "" ||
			req.Method != http.MethodGet || req.Header.Get("Authorization") != "" || req.Header.Get("Cookie") != "" ||
			req.Header.Get("Accept") != "application/json" || req.URL.User != nil {
			t.Error("unexpected request to the public provider")
		}
		payload, ok := payloads[req.URL.EscapedPath()]
		if !ok {
			t.Errorf("unexpected provider path %q", req.URL.EscapedPath())
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(payload))}, nil
	})
	t.Cleanup(func() {
		if calls.Load() != 2 {
			t.Errorf("got %d requests, want two", calls.Load())
		}
	})
	return &client
}

func TestPublicGEXUsesOldestVintageAndSuppressesUncertainFlip(t *testing.T) {
	cutoff := time.Date(2026, 11, 26, 16, 30, 0, 0, time.UTC)
	nqAsOf := cutoff.Add(-3 * time.Hour)
	client := mockPublicGEXClient(t, map[string]string{
		"/v1/stock/NQ%3DF/summary": publicGEXPayload("NQ=F", cutoff.Add(-time.Minute), cutoff.Add(-time.Hour), cutoff.Add(-2*time.Hour),
			`{"gamma_flip": 26317, "gamma_flip_status": "available", "call_wall": 27000, "put_wall": 26000, "regime": "negative_gamma"}`,
			fmt.Sprintf(`, "as_of": %q`, nqAsOf.Format(time.RFC3339Nano))),
		"/v1/stock/ES%3DF/summary": publicGEXPayload("ES=F", cutoff.Add(-time.Minute), cutoff.Add(-time.Hour), cutoff.Add(-2*time.Hour),
			`{"gamma_flip": 7363, "gamma_flip_status": "insufficient_local_coverage", "call_wall": 7400, "put_wall": 7000, "regime": "positive_gamma"}`, ""),
	})
	snapshot, err := fetchPublicGEX(context.Background(), cutoff, client)
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Validate(cutoff); err != nil {
		t.Fatal(err)
	}
	if !snapshot.ObservedAt.Equal(nqAsOf) || snapshot.Source != "FlashAlpha · options sur futures CME (différé)" || snapshot.SourceURL != "https://flashalpha.com/methodology/futures" {
		t.Fatalf("wrong provenance: %+v", snapshot)
	}
	nq := snapshot.Instruments["NQ"].Gamma
	if nq == nil || nq.Regime != "negative" || nq.Flip == nil || *nq.Flip != 26317 || nq.CallWall == nil || nq.PutWall == nil {
		t.Fatalf("NQ gamma was not imported correctly: %+v", nq)
	}
	es := snapshot.Instruments["ES"].Gamma
	if es == nil || es.Regime != "" || es.Flip != nil || es.CallWall == nil || es.PutWall == nil {
		t.Fatalf("unverified ES flip or regime was used: %+v", es)
	}
}

func TestPublicGEXRequiresFuturesIdentityAndFreshTimestamps(t *testing.T) {
	cutoff := time.Date(2026, 11, 26, 16, 30, 0, 0, time.UTC)
	fresh := cutoff.Add(-time.Hour)
	const exposure = `{"gamma_flip": 0, "gamma_flip_status": "available", "call_wall": 27000, "put_wall": -1, "regime": "positive_gamma"}`
	valid := publicGEXPayload("NQ=F", fresh, fresh, cutoff.Add(-MaxMarketSnapshotAge), exposure, "")
	es := publicGEXPayload("ES=F", fresh, fresh, fresh, exposure, "")
	for _, example := range []struct {
		name    string
		payload string
	}{
		{"wrong instrument", publicGEXPayload("SPX", fresh, fresh, fresh, exposure, "")},
		{"stale options", publicGEXPayload("ES=F", fresh, fresh, cutoff.Add(-MaxMarketSnapshotAge-time.Nanosecond), exposure, "")},
		{"future timestamp", publicGEXPayload("ES=F", cutoff.Add(time.Nanosecond), fresh, fresh, exposure, "")},
		{"missing underlying timestamp", publicGEXPayload("ES=F", fresh, time.Time{}, fresh, exposure, "")},
		{"stale instrument timestamp", publicGEXPayload("ES=F", fresh, fresh, fresh, exposure, `, "as_of": "2026-11-24T16:30:00Z"`)},
		{"not delayed", strings.Replace(es, `"delayed": true`, `"delayed": false`, 1)},
		{"no verified levels", publicGEXPayload("ES=F", fresh, fresh, fresh, `{}`, "")},
		{"oversized response", es + strings.Repeat(" ", publicGEXLimit)},
	} {
		t.Run(example.name, func(t *testing.T) {
			client := mockPublicGEXClient(t, map[string]string{
				"/v1/stock/NQ%3DF/summary": valid,
				"/v1/stock/ES%3DF/summary": example.payload,
			})
			snapshot, err := fetchPublicGEX(context.Background(), cutoff, client)
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshot.Instruments) != 1 || !snapshot.ObservedAt.Equal(cutoff.Add(-MaxMarketSnapshotAge)) {
				t.Fatalf("wanted only fresh NQ: %+v", snapshot)
			}
			nq := snapshot.Instruments["NQ"].Gamma
			if nq == nil || nq.Regime != "" || nq.Flip != nil || nq.CallWall == nil || nq.PutWall != nil {
				t.Fatalf("invalid price or unsupported regime remained: %+v", nq)
			}
		})
	}
}

func TestPublicGEXNoUsableResult(t *testing.T) {
	client := mockPublicGEXClient(t, map[string]string{
		"/v1/stock/NQ%3DF/summary": `{}`,
		"/v1/stock/ES%3DF/summary": `[]`,
	})
	if snapshot, err := fetchPublicGEX(context.Background(), time.Now(), client); snapshot != nil || !errors.Is(err, ErrPublicGEXUnavailable) {
		t.Fatalf("got snapshot %+v and error %v, want unavailable", snapshot, err)
	}
}

func TestPublicGEXRefusesRedirects(t *testing.T) {
	client := *publicGEXClient
	client.Transport = publicGEXTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "lab.flashalpha.com" {
			t.Errorf("followed redirect to %s", req.URL.Host)
		}
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{"https://other.example"}},
			Body:       http.NoBody,
		}, nil
	})
	if snapshot, err := fetchPublicGEX(context.Background(), time.Now(), &client); snapshot != nil || !errors.Is(err, errPublicGEXConnection) {
		t.Fatalf("got snapshot %+v and error %v, want sanitized connection error", snapshot, err)
	}
}
