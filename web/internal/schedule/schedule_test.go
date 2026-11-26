package schedule

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func instant(t *testing.T, value string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func openTestStore(t *testing.T, now time.Time) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "schedule.json")
	store, err := Open(path, now)
	if err != nil {
		t.Fatal(err)
	}
	return store, path
}

func settings() Config {
	return Config{Enabled: true, Time: "13:30", TimeZone: "Europe/Paris", WeekdaysOnly: true, Model: "gpt-6.2-sol", GEXMode: "without", Owner: "test-account"}
}

func update(t *testing.T, store *Store, config Config, now time.Time) {
	t.Helper()
	if err := store.Update(config, now); err != nil {
		t.Fatal(err)
	}
}

func tick(t *testing.T, store *Store, now time.Time, want bool, execute func(context.Context, Config) Result) {
	t.Helper()
	got, err := store.Tick(context.Background(), now, execute)
	if got != want || err != nil {
		t.Fatalf("Tick at %s = %t, %v; want %t, nil", now, got, err, want)
	}
}

func success(context.Context, Config) Result {
	return Result{Status: "success", BriefID: "fixture"}
}

func TestWeeklyLocalTimingAndWakeWindow(t *testing.T) {
	before := instant(t, "2026-11-27T12:25:00Z") // Friday, 13:25 Paris.
	store, _ := openTestStore(t, before)
	if defaults := store.State(); defaults.Config.Enabled || defaults.Config.Time != "13:30" ||
		!defaults.Config.WeekdaysOnly || !defaults.Next(before).IsZero() {
		t.Fatalf("unexpected new schedule: %+v", defaults)
	}
	update(t, store, settings(), before)
	if next := store.State().Next(before); !next.Equal(instant(t, "2026-11-27T12:30:00Z")) {
		t.Fatalf("next Friday = %v", next)
	}
	tick(t, store, before, false, success)
	tick(t, store, before.Add(10*time.Minute), true, success) // Inclusive five-minute wake window.
	tick(t, store, before.Add(10*time.Minute), false, success)
	last := store.State().Last
	if last == nil || last.Status != "success" || last.BriefID != "fixture" {
		t.Fatalf("last = %+v", last)
	}
	if next := store.State().Next(before.Add(10 * time.Minute)); !next.Equal(instant(t, "2026-11-30T12:30:00Z")) {
		t.Fatalf("next after Friday claim = %v", next)
	}
	tick(t, store, instant(t, "2026-11-28T12:30:00Z"), false, success)
}

func TestNoBackfillAndNoImmediateRunWhenEnablingAfterSlot(t *testing.T) {
	slot := instant(t, "2026-11-26T12:30:00Z")
	store, _ := openTestStore(t, slot)
	update(t, store, settings(), slot.Add(time.Minute))
	tick(t, store, slot.Add(2*time.Minute), false, success)
	if next := store.State().Next(slot.Add(2 * time.Minute)); !next.Equal(slot.AddDate(0, 0, 1)) {
		t.Fatalf("next after enabling late = %v", next)
	}
	nextDay := slot.AddDate(0, 0, 1)
	tick(t, store, nextDay.Add(5*time.Minute+time.Nanosecond), false, success)
	if store.State().Last != nil {
		t.Fatal("missed slots should not be claimed")
	}
}

func TestClaimSurvivesRestartAndMarksOrphanInterrupted(t *testing.T) {
	slot := instant(t, "2026-11-26T12:30:00Z")
	store, path := openTestStore(t, slot)
	update(t, store, settings(), slot.Add(-time.Minute))
	orphanPath := filepath.Join(t.TempDir(), "schedule.json")
	tick(t, store, slot, true, func(_ context.Context, c Config) Result {
		if c.Owner != "test-account" {
			t.Fatal("owner lost before execution")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var saved struct {
			Last *Result `json:"last"`
		}
		if err := json.Unmarshal(raw, &saved); err != nil || saved.Last == nil || saved.Last.Status != "running" {
			t.Fatal("no durable started claim before provider callback")
		}
		if err := os.WriteFile(orphanPath, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		return Result{Status: "success", BriefID: "fixture"}
	})
	reopened, err := Open(path, slot.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if state := reopened.State(); state.Last == nil || state.Last.BriefID != "fixture" || state.Config.Owner != settings().Owner {
		t.Fatalf("reopened = %+v", state)
	}
	tick(t, reopened, slot.Add(time.Minute), false, success)
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("schedule file must be owner-only: %v, %v", info, err)
		}
	}
	orphan, err := Open(orphanPath, slot.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if last := orphan.State().Last; last == nil || last.Status != "interrupted" {
		t.Fatalf("orphan last = %+v", last)
	}
	tick(t, orphan, slot.Add(time.Minute), false, success)
}

func TestDisableCancelsInFlightAndRejectsLateResult(t *testing.T) {
	slot := instant(t, "2026-11-26T12:30:00Z")
	store, path := openTestStore(t, slot)
	config := settings()
	update(t, store, config, slot.Add(-time.Minute))
	started := make(chan context.Context, 1)
	returned := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := store.Tick(context.Background(), slot, func(ctx context.Context, _ Config) Result {
			started <- ctx
			<-returned
			return Result{Status: "success", BriefID: "too-late"}
		})
		done <- err
	}()
	var inFlight context.Context
	select {
	case inFlight = <-started:
	case err := <-done:
		t.Fatalf("run returned before callback: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("callback did not start")
	}
	tick(t, store, slot.Add(time.Second), false, success)
	update(t, store, Config{}, slot.Add(time.Second))
	select {
	case <-inFlight.Done():
	default:
		t.Fatal("disabling did not cancel the running callback")
	}
	close(returned)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if state := store.State(); state.Config.Enabled || state.Last == nil || state.Last.Status != "cancelled" || state.Last.BriefID != "" {
		t.Fatalf("late callback overwrote disabled state: %+v, last %+v", state.Config, state.Last)
	}
	reopened, err := Open(path, slot.Add(2*time.Second))
	if err != nil || reopened.State().Config.Enabled || reopened.State().Last.Status != "cancelled" {
		t.Fatalf("disable was not persisted: %v", err)
	}
	config.Enabled = false
	if reopened.State().Config != config {
		t.Fatal("disabling erased the saved schedule settings")
	}
}

func TestClaimWriteFailurePreventsExecution(t *testing.T) {
	slot := instant(t, "2026-11-26T12:30:00Z")
	store, path := openTestStore(t, slot)
	update(t, store, settings(), slot.Add(-time.Minute))
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	count := 0
	didRun, err := store.Tick(context.Background(), slot, func(ctx context.Context, c Config) Result {
		count++
		return success(ctx, c)
	})
	if didRun || err == nil || count != 0 {
		t.Fatalf("failed claim reached provider: %t, %v, calls %d", didRun, err, count)
	}
}

func TestDaylightSavingGapAndFold(t *testing.T) {
	config := settings()
	config.Time, config.WeekdaysOnly = "02:30", false
	for _, tc := range []struct {
		name, zone, before, next, after string
	}{
		{"gap", "America/New_York", "2026-03-08T05:00:00Z", "2026-03-09T06:30:00Z", "2026-03-08T07:30:00Z"},
		{"fold", "Europe/Paris", "2026-10-24T22:00:00Z", "2026-10-25T00:30:00Z", "2026-10-25T01:31:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := instant(t, tc.before)
			store, _ := openTestStore(t, before)
			config.TimeZone = tc.zone
			update(t, store, config, before)
			if got := store.State().Next(before); !got.Equal(instant(t, tc.next)) {
				t.Fatalf("next = %v; want %v", got, tc.next)
			}
			tick(t, store, instant(t, tc.after), false, success)
		})
	}
}

func TestRejectInvalidEnabledConfig(t *testing.T) {
	now := instant(t, "2026-11-26T12:00:00Z")
	store, _ := openTestStore(t, now)
	for _, damage := range []func(*Config){
		func(c *Config) { c.Time = "9:30" },
		func(c *Config) { c.Time = "24:00" },
		func(c *Config) { c.TimeZone = "Local" },
		func(c *Config) { c.TimeZone = "../Europe/Paris" },
		func(c *Config) { c.Model = "" },
		func(c *Config) { c.Owner = "" },
		func(c *Config) { c.GEXMode = "unknown" },
	} {
		config := settings()
		damage(&config)
		if err := store.Update(config, now); err == nil {
			t.Fatalf("accepted invalid config %+v", config)
		}
	}
}
