package core

import (
	"strings"
	"testing"
	"time"
)

// Backwards-compatible behaviour: the idle auto-reset notice is posted by
// default. These tests lock the new `reset_idle_notice` toggle: "off" must
// silence only the user-visible notice, while the rotation itself (and its
// "auto-resetting idle session" log line) keeps working.

// newIdleEngineWithPlatform builds an engine wired to a reply-capturing
// platform so the user-visible notices can be asserted.
func newIdleEngineWithPlatform() (*Engine, *stubPlatformEngine) {
	p := &stubPlatformEngine{n: "test"}
	return NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish), p
}

// newStaleSession returns a locked session whose last user activity is well
// past the reset threshold, so the idle path is guaranteed to fire.
func newStaleSession(t *testing.T, sm *SessionManager, key string) *Session {
	t.Helper()
	session := sm.GetOrCreateActive(key)
	session.AddHistory("user", "stale context")
	session.SetAgentSessionID("old-session", "stub")
	staleAt := time.Now().Add(-2 * time.Hour)
	session.mu.Lock()
	session.LastUserActivity = staleAt
	session.UpdatedAt = staleAt
	session.mu.Unlock()
	session.TryLock()
	return session
}

// TestMaybeAutoResetSessionOnIdle_NoticeSuppressedWhenOff verifies that
// reset_idle_notice = "off" silences the chat notice but NOT the rotation:
// the call still returns a fresh session and the old session keeps its
// history for /list and /switch.
func TestMaybeAutoResetSessionOnIdle_NoticeSuppressedWhenOff(t *testing.T) {
	e, p := newIdleEngineWithPlatform()
	e.SetResetOnIdle(30 * time.Minute)
	e.SetResetIdleNotice(false)

	sm := NewSessionManager(t.TempDir())
	session := newStaleSession(t, sm, "u")

	rotated, _ := e.maybeAutoResetSessionOnIdle(p, &Message{SessionKey: "u", ReplyCtx: "ctx"}, sm, "ws:u", session, 0)
	if rotated == nil {
		t.Fatal("rotation must still happen when reset_idle_notice = off")
	}
	if rotated.ID == session.ID {
		t.Fatalf("rotated session ID = %s, want a new session", rotated.ID)
	}
	if sent := p.getSent(); len(sent) != 0 {
		t.Fatalf("replies = %q, want none: reset_idle_notice = off must silence the notice", sent)
	}
	if got := len(session.GetHistory(0)); got != 1 {
		t.Fatalf("old session history len = %d, want 1 preserved entry", got)
	}
	if got := session.GetAgentSessionID(); got != "old-session" {
		t.Fatalf("old session agent id = %q, want old-session preserved", got)
	}
}

// TestMaybeAutoResetSessionOnIdle_NoticeEmittedWhenOn verifies that an
// explicit reset_idle_notice = "on" keeps the historical notice.
func TestMaybeAutoResetSessionOnIdle_NoticeEmittedWhenOn(t *testing.T) {
	e, p := newIdleEngineWithPlatform()
	e.SetResetOnIdle(30 * time.Minute)
	e.SetResetIdleNotice(true)

	sm := NewSessionManager(t.TempDir())
	session := newStaleSession(t, sm, "u")

	rotated, _ := e.maybeAutoResetSessionOnIdle(p, &Message{SessionKey: "u", ReplyCtx: "ctx"}, sm, "ws:u", session, 0)
	if rotated == nil {
		t.Fatal("expected idle auto-reset to fire")
	}
	sent := p.getSent()
	if len(sent) != 1 {
		t.Fatalf("replies = %q, want exactly the idle-reset notice", sent)
	}
	if !strings.Contains(sent[0], "Session auto-reset after 30 minute(s)") {
		t.Fatalf("reply = %q, want the idle-reset notice", sent[0])
	}
}

// TestMaybeAutoResetSessionOnIdle_NoticeEnabledByDefault guards the zero
// value of the engine field: an engine that never called
// SetResetIdleNotice must keep posting the notice (backwards compatibility).
func TestMaybeAutoResetSessionOnIdle_NoticeEnabledByDefault(t *testing.T) {
	e, p := newIdleEngineWithPlatform()
	e.SetResetOnIdle(30 * time.Minute)

	sm := NewSessionManager(t.TempDir())
	session := newStaleSession(t, sm, "u")

	rotated, _ := e.maybeAutoResetSessionOnIdle(p, &Message{SessionKey: "u", ReplyCtx: "ctx"}, sm, "ws:u", session, 0)
	if rotated == nil {
		t.Fatal("expected idle auto-reset to fire")
	}
	sent := p.getSent()
	if len(sent) != 1 || !strings.Contains(sent[0], "Session auto-reset") {
		t.Fatalf("replies = %q, want the idle-reset notice by default", sent)
	}
}
