package core

import (
	"path/filepath"
	"testing"
	"time"
)

// Regression tests for issue #1731.
//
// Background: previously, /switch <n> into a long-idle session would leave
// the very first post-switch message routed to a brand-new session because
// reset_on_idle_mins still compared against the session's last user activity,
// which could be hours ago. The fix records an explicit-activation
// timestamp on /switch (and SwitchToAgentSession) and treats it as the
// baseline for the idle decision — but caps the exemption at
// ExplicitActivationTTL so abandoned sessions cannot permanently occupy the
// active slot.

// TestSwitchSession_MarksExplicitlyActivated verifies that the user-facing
// SwitchSession path records an explicit-activation timestamp on the target
// session, while NOT touching unrelated sessions for the same user.
func TestSwitchSession_MarksExplicitlyActivated(t *testing.T) {
	sm := NewSessionManager(t.TempDir())
	// Build two existing sessions.
	a := sm.GetOrCreateActive("u")
	a.AddHistory("user", "old-a")
	a.SetAgentSessionID("aid-a", "claudecode")

	b := sm.NewSession("u", "second")
	b.AddHistory("user", "old-b")
	b.SetAgentSessionID("aid-b", "claudecode")

	before := time.Now()
	got, err := sm.SwitchSession("u", b.ID)
	after := time.Now()
	if err != nil {
		t.Fatalf("SwitchSession: %v", err)
	}
	if got.ID != b.ID {
		t.Fatalf("SwitchSession returned %s, want %s", got.ID, b.ID)
	}

	activatedAt := got.GetExplicitActivatedAt()
	if activatedAt.IsZero() {
		t.Fatalf("ExplicitActivatedAt must be set after SwitchSession")
	}
	if activatedAt.Before(before) || activatedAt.After(after) {
		t.Fatalf("ExplicitActivatedAt = %v, want between %v and %v", activatedAt, before, after)
	}
	if !a.GetExplicitActivatedAt().IsZero() {
		t.Fatalf("unrelated session A should not be marked; got %v", a.GetExplicitActivatedAt())
	}
}

// TestSwitchToAgentSession_MarksExplicitlyActivated verifies that the
// internal SwitchToAgentSession path also records explicit activation, so
// any caller that already had agent session IDs in flight benefits from the
// same exemption.
func TestSwitchToAgentSession_MarksExplicitlyActivated(t *testing.T) {
	sm := NewSessionManager(t.TempDir())
	s := sm.SwitchToAgentSession("u", "aid-1", "claudecode", "first")
	if s.GetExplicitActivatedAt().IsZero() {
		t.Fatal("SwitchToAgentSession must mark explicit activation")
	}
}

// TestMaybeAutoResetSessionOnIdle_ExplicitActivationBlocksReset reproduces
// the exact symptom from #1731: the session's LastUserActivity is older than
// reset_on_idle_mins, but the user has just /switch-ed into it — the reset
// must NOT fire on the next message.
func TestMaybeAutoResetSessionOnIdle_ExplicitActivationBlocksReset(t *testing.T) {
	e := newTestEngine()
	e.SetResetOnIdle(30 * time.Minute)

	sm := NewSessionManager(t.TempDir())
	session := sm.GetOrCreateActive("u")
	session.AddHistory("user", "old msg")
	session.SetAgentSessionID("aid", "claudecode")
	session.TryLock()

	// Session's last real user message was 2 hours ago.
	old := time.Now().Add(-2 * time.Hour)
	session.mu.Lock()
	session.LastUserActivity = old
	session.UpdatedAt = old
	// But the user just /switch-ed into it — explicit activation is fresh.
	session.markExplicitlyActivatedLocked()
	session.mu.Unlock()

	p := &stubPlatformEngine{n: "test"}
	msg := &Message{SessionKey: "u", ReplyCtx: "ctx"}

	rotated := e.maybeAutoResetSessionOnIdle(p, msg, sm, "ws:u", session)
	if rotated != nil {
		t.Fatal("explicitly activated session must NOT be rotated even if LastUserActivity is old")
	}
}

// TestMaybeAutoResetSessionOnIdle_ExplicitActivationExpiresAfterTTL is the
// safety net: a long-abandoned session that was /switch-ed into once must
// still be rotated away after ExplicitActivationTTL even though the
// explicit-activation timestamp is recent-looking (because we artificially
// age it past the TTL).
func TestMaybeAutoResetSessionOnIdle_ExplicitActivationExpiresAfterTTL(t *testing.T) {
	e := newTestEngine()
	e.SetResetOnIdle(30 * time.Minute)

	sm := NewSessionManager(t.TempDir())
	session := sm.GetOrCreateActive("u")
	session.AddHistory("user", "old")
	session.SetAgentSessionID("aid", "claudecode")
	session.TryLock()

	old := time.Now().Add(-2 * time.Hour)
	session.mu.Lock()
	session.LastUserActivity = old
	session.UpdatedAt = old
	session.mu.Unlock()

	// Simulate an explicit activation from 8 days ago — past the 7-day TTL.
	session.mu.Lock()
	session.ExplicitActivatedAt = time.Now().Add(-8 * 24 * time.Hour)
	session.mu.Unlock()

	p := &stubPlatformEngine{n: "test"}
	msg := &Message{SessionKey: "u", ReplyCtx: "ctx"}

	rotated := e.maybeAutoResetSessionOnIdle(p, msg, sm, "ws:u", session)
	if rotated == nil {
		t.Fatal("explicit activation older than TTL must NOT block the idle reset")
	}
}

// TestMaybeAutoResetSessionOnIdle_NonExplicitSessionUsesLegacyPath confirms
// that sessions which were never explicitly activated (e.g. the implicit
// "default" session that every user starts with) keep the pre-#1731
// behaviour: the reset fires when LastUserActivity is older than
// reset_on_idle_mins.
func TestMaybeAutoResetSessionOnIdle_NonExplicitSessionUsesLegacyPath(t *testing.T) {
	e := newTestEngine()
	e.SetResetOnIdle(30 * time.Minute)

	sm := NewSessionManager(t.TempDir())
	session := sm.GetOrCreateActive("u")
	session.AddHistory("user", "old")
	session.SetAgentSessionID("aid", "claudecode")
	session.TryLock()

	if !session.GetExplicitActivatedAt().IsZero() {
		t.Fatal("freshly-created active session should not yet be marked as explicitly activated")
	}

	old := time.Now().Add(-2 * time.Hour)
	session.mu.Lock()
	session.LastUserActivity = old
	session.UpdatedAt = old
	session.mu.Unlock()

	p := &stubPlatformEngine{n: "test"}
	msg := &Message{SessionKey: "u", ReplyCtx: "ctx"}

	rotated := e.maybeAutoResetSessionOnIdle(p, msg, sm, "ws:u", session)
	if rotated == nil {
		t.Fatal("non-explicit session with old LastUserActivity must be rotated (legacy path)")
	}
}

// TestSwitchSession_PreventsFirstMessageRotation is the end-to-end shape of
// the bug report: user has two sessions; one is idle 2 hours, the other
// active 5 minutes ago. User /switch to the idle one. The very next message
// must NOT be rotated to a fresh session.
func TestSwitchSession_PreventsFirstMessageRotation(t *testing.T) {
	e := newTestEngine()
	e.SetResetOnIdle(30 * time.Minute)

	sm := NewSessionManager(t.TempDir())

	// Build session "fresh" — last activity 5 min ago, no explicit activation.
	fresh := sm.GetOrCreateActive("u")
	fresh.AddHistory("user", "hi")
	fresh.SetAgentSessionID("aid-fresh", "claudecode")
	fresh.mu.Lock()
	fresh.LastUserActivity = time.Now().Add(-5 * time.Minute)
	fresh.mu.Unlock()

	// Build session "stale" — last activity 2 hours ago.
	stale := sm.NewSession("u", "stale")
	stale.AddHistory("user", "old")
	stale.SetAgentSessionID("aid-stale", "claudecode")
	stale.mu.Lock()
	stale.LastUserActivity = time.Now().Add(-2 * time.Hour)
	stale.UpdatedAt = time.Now().Add(-2 * time.Hour)
	stale.mu.Unlock()

	// /switch to stale.
	if _, err := sm.SwitchSession("u", stale.ID); err != nil {
		t.Fatalf("SwitchSession: %v", err)
	}

	// Now the very next message lands on stale.
	current := sm.GetOrCreateActive("u")
	if current.ID != stale.ID {
		t.Fatalf("GetOrCreateActive returned %s, want stale %s", current.ID, stale.ID)
	}
	if !current.TryLock() {
		t.Fatal("session should be lockable")
	}

	p := &stubPlatformEngine{n: "test"}
	msg := &Message{SessionKey: "u", ReplyCtx: "ctx"}

	rotated := e.maybeAutoResetSessionOnIdle(p, msg, sm, "ws:u", current)
	if rotated != nil {
		t.Fatal("post-switch first message must stay in the explicitly chosen session")
	}
}

// TestExplicitActivatedAt_PersistsAcrossSessionRestore verifies that the
// new field survives the JSON snapshot/restore path (issue #1731 would
// regress after a process restart otherwise).
func TestExplicitActivatedAt_PersistsAcrossSessionRestore(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "session.json")
	sm := NewSessionManager(storePath)
	session := sm.GetOrCreateActive("u")
	session.AddHistory("user", "hi")
	session.SetAgentSessionID("aid", "claudecode")
	session.MarkExplicitlyActivated()
	original := session.GetExplicitActivatedAt()
	if original.IsZero() {
		t.Fatal("setup: explicit activation not set")
	}
	// Persist the explicit-activation timestamp to disk; in production this
	// happens because callers that mutate session state go through paths that
	// already call sm.Save(). For the test we trigger it explicitly.
	sm.Save()

	// Reload from disk.
	sm2 := NewSessionManager(storePath)
	restored := sm2.GetOrCreateActive("u")
	if restored.GetExplicitActivatedAt().IsZero() {
		t.Fatal("ExplicitActivatedAt did not survive snapshot/restore")
	}
	// Truncate to second precision to match JSON round-trip behaviour.
	if !restored.GetExplicitActivatedAt().Truncate(time.Second).Equal(original.Truncate(time.Second)) {
		t.Fatalf("ExplicitActivatedAt drifted: got %v, want %v", restored.GetExplicitActivatedAt(), original)
	}
}