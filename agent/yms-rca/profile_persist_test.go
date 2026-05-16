package ymsagent

import (
	"path/filepath"
	"testing"
)

// newTestSessionWithStore wires a fresh per-test profileStore + project +
// sessionKey onto a non-subprocess session.
func newTestSessionWithStore(t *testing.T, project, sessionKey string) (*session, *profileStore) {
	t.Helper()
	s, _ := newTestSession(t, "default")
	store := newProfileStore(filepath.Join(t.TempDir(), "store.json"))
	s.profileStore = store
	s.project = project
	s.sessionKey = sessionKey
	return s, store
}

func TestUpdateCurrentProfilePersistsNonLocalProfile(t *testing.T) {
	s, store := newTestSessionWithStore(t, "yms-rca-youzone", "youzone:conv:user")

	s.updateCurrentProfile("pre")

	if got := store.Get("yms-rca-youzone", "youzone:conv:user"); got != "pre" {
		t.Errorf("store.Get = %q, want pre", got)
	}
	if got := s.currentProfileName(); got != "pre" {
		t.Errorf("currentProfileName = %q, want pre", got)
	}
}

func TestUpdateCurrentProfileClearsLocalProfile(t *testing.T) {
	s, store := newTestSessionWithStore(t, "yms-rca-youzone", "youzone:conv:user")
	store.Set("yms-rca-youzone", "youzone:conv:user", "pre")

	s.updateCurrentProfile("local")

	if got := store.Get("yms-rca-youzone", "youzone:conv:user"); got != "" {
		t.Errorf("after switch to local, store should be cleared, got %q", got)
	}
	if got := s.currentProfileName(); got != "local" {
		t.Errorf("currentProfileName = %q, want local", got)
	}
}

func TestUpdateCurrentProfileNoStoreIsNoOp(t *testing.T) {
	// Without a store wired (e.g. older code path / unit-test session), the
	// existing in-memory profile update path must still work.
	s, _ := newTestSession(t, "default")
	s.updateCurrentProfile("pre")
	if got := s.currentProfileName(); got != "pre" {
		t.Errorf("currentProfileName = %q, want pre", got)
	}
}

func TestUpdateCurrentProfileNoProjectOrSessionKeyIsNoOp(t *testing.T) {
	// If extraEnv didn't carry CC_PROJECT/CC_SESSION_KEY (programmatic test,
	// CLI relay not attached), store stays untouched but in-memory profile
	// still updates.
	s, store := newTestSessionWithStore(t, "", "")
	s.updateCurrentProfile("pre")
	if got := s.currentProfileName(); got != "pre" {
		t.Errorf("currentProfileName = %q, want pre", got)
	}
	// store has nothing under "" keys (Set rejects empty)
	if got := store.Get("", ""); got != "" {
		t.Errorf("store should reject empty keys, got %q", got)
	}
}

func TestNewSessionExtractsProjectAndSessionKeyFromEnv(t *testing.T) {
	// Using the buildSessionEnv pure helper isn't enough — we need the
	// session struct's project/sessionKey populated from extraEnv. Verify
	// via the parser used by newSession.
	project, sessionKey := parseProjectAndSessionKey([]string{
		"PATH=/usr/bin",
		"CC_PROJECT=yms-rca-youzone",
		"CC_SESSION_KEY=youzone:claw_123:5837619",
		"OTHER=value",
	})
	if project != "yms-rca-youzone" {
		t.Errorf("project = %q, want yms-rca-youzone", project)
	}
	if sessionKey != "youzone:claw_123:5837619" {
		t.Errorf("sessionKey = %q, want youzone:claw_123:5837619", sessionKey)
	}
}

func TestParseProjectAndSessionKeyMissingFields(t *testing.T) {
	project, sessionKey := parseProjectAndSessionKey([]string{"PATH=/usr/bin"})
	if project != "" || sessionKey != "" {
		t.Errorf("missing env should yield empty, got project=%q key=%q", project, sessionKey)
	}
}
