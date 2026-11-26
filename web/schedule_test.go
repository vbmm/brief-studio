package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"briefstudio/web/internal/chatgpt"
	"briefstudio/web/internal/schedule"
)

// The stored fixture models a locally verified OAuth record. No provider
// requests are needed to test consent, ownership and persisted settings.
type scheduleFixtureVault struct{ data []byte }

func (v *scheduleFixtureVault) Load() ([]byte, error) {
	return append([]byte{}, v.data...), nil
}
func (v *scheduleFixtureVault) Save([]byte) error { return nil }
func (v *scheduleFixtureVault) Delete() error     { return nil }

func schedulingFixtureSession(t *testing.T, s *studioServer, subject string) *chatgpt.Session {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"version": 1, "host_id": s.library.hostID, "client_id": "oaiapp_fixture", "subject": subject,
		"id_token": "fixture_identity", "access_token": "fixture_access", "refresh_token": "fixture_refresh",
		"expiry": time.Now().Add(time.Hour), "scope": "openid offline_access resource.invoke chatgpt.tokens.use.direct",
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := s.client.RestoreSession(s.library.hostID, &scheduleFixtureVault{data: data})
	if err != nil || session.ScheduleOwner() == "" {
		t.Fatalf("could not restore scheduling test session: %v", err)
	}
	return session
}

func TestScheduleUsesExplicitAccountConsentAndCanBePaused(t *testing.T) {
	s := testServer(t)
	path := filepath.Join(t.TempDir(), "schedule.json")
	store, err := schedule.Open(path, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s.schedule = store
	cookie := &http.Cookie{Name: s.cookieName, Value: s.browserKey}
	const route = "/api/schedule"
	const authorized = `{"enabled":true,"consent":true,"time":"13:30","timeZone":"Europe/Paris","weekdaysOnly":true,"model":"fixture-model","gexMode":"without"}`
	if w := request(s, "POST", route, authorized, nil); w.Code != http.StatusUnauthorized {
		t.Fatal("schedule reused an unauthenticated loopback request")
	}
	if w := request(s, "GET", route, "", cookie); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatal("existing profiles should start with no active schedule")
	}
	if w := request(s, "POST", route, authorized, cookie); w.Code != http.StatusUnauthorized {
		t.Fatal("schedule accepted a missing ChatGPT account")
	}
	s.session = schedulingFixtureSession(t, s, "fixture-subject")
	s.models = []chatgpt.Model{{ID: "fixture-model"}}
	withoutConsent := strings.Replace(authorized, `"consent":true`, `"consent":false`, 1)
	if w := request(s, "POST", route, withoutConsent, cookie); w.Code != http.StatusBadRequest ||
		!strings.Contains(w.Body.String(), "forfait ChatGPT") {
		t.Fatal("schedule enabled without affirmative consent")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("rejected request wrote schedule")
	}
	if w := request(s, "POST", route, authorized, cookie); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"enabled":true`) || !strings.Contains(w.Body.String(), `"next":`) ||
		strings.Contains(w.Body.String(), s.session.ScheduleOwner()) || strings.Contains(w.Body.String(), "fixture_refresh") {
		t.Fatalf("schedule was not safely enabled: %d", w.Code)
	}
	reloaded, err := schedule.Open(path, time.Now())
	if err != nil || !reloaded.State().Config.Enabled || reloaded.State().Config.Owner != s.session.ScheduleOwner() {
		t.Fatalf("restored schedule is not bound to its originating account: %v", err)
	}
	s.schedule = reloaded
	s.session = schedulingFixtureSession(t, s, "another-subject")
	if w := request(s, "GET", route, "", cookie); !strings.Contains(w.Body.String(), `"needsConsent":true`) ||
		strings.Contains(w.Body.String(), `"next":`) {
		t.Fatal("switched account inherited a prior schedule")
	}
	if result := s.runScheduledBrief(context.Background(), reloaded.State().Config); result.Status != "blocked" {
		t.Fatal("different account should be blocked before provider calls")
	}
	s.session = nil
	if w := request(s, "POST", route, `{"enabled":false}`, cookie); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"enabled":false`) || s.schedule.State().Config.Time != "13:30" {
		t.Fatal("pausing must work after losing connection and keep the chosen hour")
	}
}
