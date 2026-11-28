package chatgpt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"briefstudio/web/internal/sessionvault"
)

type credentialRecord struct {
	Version  int       `json:"version"`
	HostID   string    `json:"host_id"`
	ClientID string    `json:"client_id"`
	Subject  string    `json:"subject"`
	Profile  Profile   `json:"profile"`
	IDToken  string    `json:"id_token,omitempty"`
	Access   string    `json:"access_token,omitempty"`
	Refresh  string    `json:"refresh_token,omitempty"`
	Expiry   time.Time `json:"expiry,omitempty"`
	Scope    string    `json:"scope,omitempty"`
}

// RestoreSession reads a locally verified account record. The first provider
// operation will refresh its token if expired, without browser interaction.
func (c *Client) RestoreSession(hostID string, store sessionvault.Store) (*Session, error) {
	if store == nil {
		return nil, nil
	}
	data, err := store.Load()
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, sessionvault.ErrUnavailable
	}
	defer clear(data)
	var record credentialRecord
	if json.Unmarshal(data, &record) != nil || record.Version != 1 ||
		!uuidHost.MatchString(hostID) || hostID != record.HostID ||
		!clientID.MatchString(record.ClientID) || !validSubject(record.Subject) {
		return nil, sessionvault.ErrUnavailable
	}
	var saved tokens
	if record.Access != "" {
		if !oauthToken(record.Access) || !oauthToken(record.Refresh) || !oauthToken(record.IDToken) ||
			len(record.Scope) > 4096 || record.Expiry.IsZero() {
			return nil, sessionvault.ErrUnavailable
		}
		saved = tokens{access: record.Access, refresh: record.Refresh, scope: record.Scope, expiry: record.Expiry}
	} else if record.Refresh != "" || record.IDToken != "" || record.Scope != "" || !record.Expiry.IsZero() {
		return nil, sessionvault.ErrUnavailable
	}
	profile := Profile{
		Name: boundedLabel(record.Profile.Name, 200), Email: boundedLabel(record.Profile.Email, 320),
		subject: record.Subject, CanUsePlan: saved.canUsePlan(),
	}
	lifetime, cancel := context.WithCancel(context.Background())
	return &Session{profile: profile, clientID: record.ClientID, hostID: hostID, idToken: record.IDToken,
		tokens: saved, store: store, remembered: saved.access != "", now: c.now, lifetime: lifetime, cancel: cancel}, nil
}

// Remember stores the verified registration and tokens as a single record.
// A storage failure keeps the session usable for this launch.
func (s *Session) Remember(store sessionvault.Store) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store = store
	s.persistLocked()
}

type PersistenceStatus string

const (
	PersistenceSignedOut        PersistenceStatus = "signed-out"
	PersistenceRemembered       PersistenceStatus = "remembered"
	PersistenceTemporary        PersistenceStatus = "temporary"
	PersistenceVaultUnavailable PersistenceStatus = "vault-unavailable"
	PersistenceReauthNeeded     PersistenceStatus = "reauth-needed"
)

// SessionState is safe to return to the local browser. The retry capability
// refers only to a local vault that can be tried without signing in again.
type SessionState struct {
	Profile          Profile
	Connected        bool
	Remembered       bool
	Persistence      PersistenceStatus
	CanRetryRemember bool
}

func (s *Session) State() SessionState {
	if s == nil {
		return SessionState{Persistence: PersistenceSignedOut}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stateLocked()
}

func (s *Session) stateLocked() SessionState {
	if s.lifetime == nil || s.lifetime.Err() != nil {
		return SessionState{Persistence: PersistenceSignedOut}
	}
	if !s.tokens.usable(s.currentTime()) {
		return SessionState{Persistence: PersistenceReauthNeeded}
	}
	state := SessionState{Profile: s.profile, Connected: true}
	switch {
	case s.tokens.refresh == "":
		state.Persistence = PersistenceTemporary
	case s.remembered:
		state.Remembered = true
		state.Persistence = PersistenceRemembered
	default:
		state.Persistence = PersistenceVaultUnavailable
		state.CanRetryRemember = s.store != nil
	}
	return state
}

// Status reports account state without exposing credentials or account keys.
func (s *Session) Status() (profile Profile, connected, remembered bool) {
	state := s.State()
	return state.Profile, state.Connected, state.Remembered
}

// RetryRemember attempts one local vault save with the current refreshable
// grant. It does not send an OAuth request or prompt for an account password.
func (s *Session) RetryRemember(ctx context.Context) PersistenceStatus {
	if s == nil {
		return PersistenceSignedOut
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.stateLocked()
	if state.CanRetryRemember && ctx.Err() == nil {
		s.persistLocked()
		state = s.stateLocked()
	}
	return state.Persistence
}

func (s *Session) currentTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// ScheduleOwner binds local consent to this verified registration. Never
// return this binding to a browser or use it as an authentication credential.
func (s *Session) ScheduleOwner() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lifetime == nil || s.lifetime.Err() != nil || !s.tokens.usable(s.currentTime()) || !s.tokens.canUsePlan() ||
		!uuidHost.MatchString(s.hostID) || !clientID.MatchString(s.clientID) || !validSubject(s.profile.subject) {
		return ""
	}
	digest := sha256.Sum256([]byte(s.hostID + "\x00" + s.clientID + "\x00" + s.profile.subject))
	return hex.EncodeToString(digest[:])
}

func (s *Session) persistLocked() {
	s.remembered = false
	if s.store == nil {
		return
	}
	record := credentialRecord{
		Version: 1, HostID: s.hostID, ClientID: s.clientID, Subject: s.profile.subject, Profile: s.profile,
		IDToken: s.idToken, Access: s.tokens.access, Refresh: s.tokens.refresh, Expiry: s.tokens.expiry, Scope: s.tokens.scope,
	}
	// Identity-only grants without a refresh token are usable for this launch.
	if record.Refresh == "" {
		record.IDToken, record.Access, record.Scope, record.Expiry = "", "", "", time.Time{}
	}
	data, err := json.Marshal(record)
	defer clear(data)
	if err != nil || s.store.Save(data) != nil {
		// A previous on-disk token may already have been spent in a rotation.
		_ = s.store.Delete()
		return
	}
	s.remembered = record.Access != ""
}

func validSubject(subject string) bool {
	return subject != "" && len(subject) <= 512 && strings.TrimSpace(subject) == subject
}
