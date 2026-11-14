package core

import (
	"strings"
	"testing"
	"time"
)

// lockedIdleSessionWithLiveAgent returns a locked session that is past the
// idle threshold and whose interactive state still holds a live agent, so a
// rotation would normally post both the graceful-close notice and the
// auto-reset notice.
func lockedIdleSessionWithLiveAgent(t *testing.T, e *Engine, sm *SessionManager, p Platform, key string) (*Session, uint64) {
	t.Helper()
	session := sm.GetOrCreateActive(key)
	session.AddHistory("user", "hello")
	session.SetAgentSessionID("agent-id-1", "stub")
	gen, ok := session.TryLock()
	if !ok {
		t.Fatal("could not lock the idle session")
	}
	session.mu.Lock()
	session.LastUserActivity = time.Now().Add(-35 * time.Minute)
	session.mu.Unlock()

	e.interactiveMu.Lock()
	e.interactiveStates[key] = &interactiveState{
		platform:     p,
		replyCtx:     "ctx",
		agentSession: newResultAgentSession("unused"),
	}
	e.interactiveMu.Unlock()
	return session, gen
}

// With idle_reset_notice = false (#2169) the session still rotates after the
// idle threshold, but neither the graceful-close notice nor the auto-reset
// notice is posted to the chat.
func TestMaybeAutoResetSessionOnIdle_NoticeDisabled_RotatesWithoutPosting(t *testing.T) {
	e := newTestEngine()
	e.SetResetOnIdle(30 * time.Minute)
	e.SetIdleResetNotice(false)

	sm := NewSessionManager("")
	p := &stubPlatformEngine{n: "test"}
	key := "test:user1"
	old, gen := lockedIdleSessionWithLiveAgent(t, e, sm, p, key)

	rotated, _ := e.maybeAutoResetSessionOnIdle(p, &Message{SessionKey: key, ReplyCtx: "ctx"}, sm, key, old, gen)
	if rotated == nil {
		t.Fatal("expected the idle session to rotate even with the notice turned off")
	}
	if rotated.ID == old.ID {
		t.Fatalf("rotated session ID = %s, want a new session", rotated.ID)
	}
	if sent := p.getSent(); len(sent) != 0 {
		t.Fatalf("posted %d message(s) with idle_reset_notice off, want none: %q", len(sent), sent)
	}
}

// The notices stay on unless a project turns them off, so existing configs
// keep their behavior.
func TestMaybeAutoResetSessionOnIdle_NoticeEnabledByDefault_PostsBothNotices(t *testing.T) {
	e := newTestEngine()
	e.SetResetOnIdle(30 * time.Minute)

	sm := NewSessionManager("")
	p := &stubPlatformEngine{n: "test"}
	key := "test:user1"
	old, gen := lockedIdleSessionWithLiveAgent(t, e, sm, p, key)

	rotated, _ := e.maybeAutoResetSessionOnIdle(p, &Message{SessionKey: key, ReplyCtx: "ctx"}, sm, key, old, gen)
	if rotated == nil {
		t.Fatal("expected the idle session to rotate")
	}
	sent := p.getSent()
	if len(sent) != 2 {
		t.Fatalf("posted %d message(s), want the graceful-close and auto-reset notices: %q", len(sent), sent)
	}
	if !strings.Contains(sent[0], "Wrapping up your previous session") {
		t.Errorf("first message = %q, want the graceful-close notice", sent[0])
	}
	if !strings.Contains(sent[1], "Session auto-reset") {
		t.Errorf("second message = %q, want the auto-reset notice", sent[1])
	}
}
