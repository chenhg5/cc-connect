package core

import (
	"context"
	"testing"
	"time"
)

// runTurn processes a single message to completion so an interactiveState
// exists with a live agent session and no active turn. It synchronizes on
// Send before emitting EventResult so the event cannot be drained as stale.
func runTurn(t *testing.T, e *Engine, p Platform, sess *blockingSendAgentSession, sessionKey, content string) {
	t.Helper()
	session := e.sessions.GetOrCreateActive(sessionKey)
	if !session.TryLock() {
		t.Fatal("expected session lock")
	}
	done := make(chan struct{})
	go func() {
		e.processInteractiveMessageWith(p, &Message{
			SessionKey: sessionKey,
			UserID:     "user1",
			Content:    content,
			ReplyCtx:   "ctx",
		}, session, e.agent, e.sessions, sessionKey, "", sessionKey)
		close(done)
	}()
	select {
	case <-sess.sendStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("Send did not start")
	}
	close(sess.unblock)
	sess.events <- Event{Type: EventResult, Content: "done", Done: true}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("turn did not complete")
	}
}

func interactiveStateExists(e *Engine, sessionKey string) bool {
	e.interactiveMu.Lock()
	defer e.interactiveMu.Unlock()
	_, ok := e.interactiveStates[sessionKey]
	return ok
}

func TestIdleExit_ReapsIdleInteractiveSession(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	sess := newBlockingSendSession("idle-1")
	e := NewEngine("test", &controllableAgent{nextSession: sess}, []Platform{p}, "", LangEnglish)
	e.idleExit = 30 * time.Millisecond

	sessionKey := "test:user1"
	runTurn(t, e, p, sess, sessionKey, "hello")

	if !interactiveStateExists(e, sessionKey) {
		t.Fatal("expected interactive state after turn")
	}

	time.Sleep(60 * time.Millisecond)
	e.reapIdleInteractiveSessions()

	if sess.Alive() {
		t.Fatal("expected idle agent session to be closed")
	}
	if interactiveStateExists(e, sessionKey) {
		t.Fatal("expected interactive state to be removed after reap")
	}
	// The saved agent session ID must survive the reap so the next message
	// can resume the conversation.
	session := e.sessions.GetOrCreateActive(sessionKey)
	if got := session.GetAgentSessionID(); got != "idle-1" {
		t.Fatalf("agent session ID lost after reap: got %q, want %q", got, "idle-1")
	}
}

func TestIdleExit_SkipsRecentActivity(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	sess := newBlockingSendSession("recent-1")
	e := NewEngine("test", &controllableAgent{nextSession: sess}, []Platform{p}, "", LangEnglish)
	e.idleExit = 1 * time.Hour

	sessionKey := "test:user1"
	runTurn(t, e, p, sess, sessionKey, "hello")

	e.reapIdleInteractiveSessions()

	if !sess.Alive() {
		t.Fatal("reaper closed a recently active session")
	}
	if !interactiveStateExists(e, sessionKey) {
		t.Fatal("reaper removed state for a recently active session")
	}
}

func TestIdleExit_DisabledWhenZero(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	sess := newBlockingSendSession("disabled-1")
	e := NewEngine("test", &controllableAgent{nextSession: sess}, []Platform{p}, "", LangEnglish)
	// idleExit left at zero: reaping disabled.

	sessionKey := "test:user1"
	runTurn(t, e, p, sess, sessionKey, "hello")

	time.Sleep(30 * time.Millisecond)
	e.reapIdleInteractiveSessions()

	if !sess.Alive() {
		t.Fatal("reaper closed a session while idle exit is disabled")
	}
}

func TestIdleExit_SkipsActiveTurn(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	sess := newBlockingSendSession("busy-1")
	e := NewEngine("test", &controllableAgent{nextSession: sess}, []Platform{p}, "", LangEnglish)
	e.idleExit = 30 * time.Millisecond

	sessionKey := "test:user1"
	session := e.sessions.GetOrCreateActive(sessionKey)
	if !session.TryLock() {
		t.Fatal("expected session lock")
	}
	done := make(chan struct{})
	go func() {
		e.processInteractiveMessageWith(p, &Message{
			SessionKey: sessionKey,
			UserID:     "user1",
			Content:    "long task",
			ReplyCtx:   "ctx",
		}, session, e.agent, e.sessions, sessionKey, "", sessionKey)
		close(done)
	}()

	select {
	case <-sess.sendStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("Send did not reach blocking wait")
	}

	time.Sleep(60 * time.Millisecond)
	e.reapIdleInteractiveSessions()

	if !sess.Alive() {
		t.Fatal("idle reaper closed a session with an active turn")
	}
	if !interactiveStateExists(e, sessionKey) {
		t.Fatal("idle reaper removed interactive state for an active turn")
	}

	close(sess.unblock)
	sess.events <- Event{Type: EventResult, Content: "done", Done: true}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("turn did not complete after unblock")
	}
}

func TestIdleExit_SkipsUnsolicitedActiveTurn(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	sess := newBlockingSendSession("unsol-1")
	e := NewEngine("test", &controllableAgent{nextSession: sess}, []Platform{p}, "", LangEnglish)
	e.idleExit = 30 * time.Millisecond

	sessionKey := "test:user1"
	runTurn(t, e, p, sess, sessionKey, "hello")

	// Emit an unsolicited event with no EventResult: the background reader
	// marks the turn active, which must block reaping even after the idle
	// cutoff passes.
	sess.events <- Event{Type: EventText, Content: "background work..."}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		e.interactiveMu.Lock()
		state := e.interactiveStates[sessionKey]
		e.interactiveMu.Unlock()
		if state != nil {
			state.mu.Lock()
			active := state.activeTurns > 0
			state.mu.Unlock()
			if active {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	time.Sleep(60 * time.Millisecond)
	e.reapIdleInteractiveSessions()

	if !sess.Alive() {
		t.Fatal("idle reaper closed a session with an active unsolicited turn")
	}
	if !interactiveStateExists(e, sessionKey) {
		t.Fatal("idle reaper removed interactive state during unsolicited turn")
	}
}

func TestIdleExit_ResumesWithSavedSessionID(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	first := newBlockingSendSession("resume-1")
	agent := &controllableAgent{nextSession: first}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.idleExit = 30 * time.Millisecond

	sessionKey := "test:user1"
	runTurn(t, e, p, first, sessionKey, "hello")

	time.Sleep(60 * time.Millisecond)
	e.reapIdleInteractiveSessions()
	if first.Alive() {
		t.Fatal("expected first session closed by reaper")
	}

	// Next message must resume with the agent session ID saved before the reap.
	var gotResumeID string
	second := newBlockingSendSession("resume-1")
	agent.startSessionFn = func(_ context.Context, sessionID string) (AgentSession, error) {
		gotResumeID = sessionID
		return second, nil
	}
	runTurn(t, e, p, second, sessionKey, "are you back?")

	if gotResumeID != "resume-1" {
		t.Fatalf("resume used session ID %q, want %q", gotResumeID, "resume-1")
	}
	if !second.Alive() {
		t.Fatal("expected resumed session to be alive")
	}
}

func TestIdleExit_SetIdleExitIgnoresNonPositive(t *testing.T) {
	e := NewEngine("test", &controllableAgent{}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangEnglish)
	e.SetIdleExit(0)
	if e.idleExit != 0 {
		t.Fatal("SetIdleExit(0) must leave idle exit disabled")
	}
	e.SetIdleExit(-time.Minute)
	if e.idleExit != 0 {
		t.Fatal("SetIdleExit with negative duration must leave idle exit disabled")
	}
}
