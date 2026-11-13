package core

import (
	"path/filepath"
	"testing"
	"time"
)

// reviveSession stays "alive" after Close so the restart path can be exercised
// without a real process.
type reviveSession struct{ *stopTestSession }

func (s *reviveSession) Close() error  { return nil }
func (s *reviveSession) Alive() bool   { return true }
func (s *reviveSession) CurrentSessionID() string { return "revive" }

func newRestartEngine(t *testing.T) (*Engine, *stubPlatformEngine, *reviveSession, *Session, *SessionManager) {
	t.Helper()
	p := &stubPlatformEngine{n: "test"}
	sess := &reviveSession{newStopTestSession()}
	e := NewEngine("test", &resultAgent{session: sess}, []Platform{p}, filepath.Join(t.TempDir(), "s.json"), LangChinese)
	t.Cleanup(e.cancel)
	sessions := e.sessions
	session := sessions.GetOrCreateActive("k")
	return e, p, sess, session, sessions
}

// Steering into a session the user stopped must be refused: the write would
// land in a pipe nobody reads, producing the "已插话但没回应" symptom.
func TestSteerRefusedAfterStop(t *testing.T) {
	e, p, sess, session, sessions := newRestartEngine(t)
	state := &interactiveState{agentSession: sess, platform: p, replyCtx: "ctx", turnActive: true, needsRestart: true}
	e.interactiveStates["k"] = state

	msg := &Message{SessionKey: "k", Content: "follow up", ReplyCtx: "ctx", Platform: "test"}
	if e.steerBusySession(p, msg, "k", session, sessions) {
		t.Fatal("steerBusySession() = true for a session stopped by the user")
	}
}

// A follow-up after a stop must be re-dispatched as a fresh turn once the old
// turn releases the lock.
func TestRestartWedgedSessionReplaysMessage(t *testing.T) {
	e, p, sess, session, sessions := newRestartEngine(t)
	session.TryLock() // simulate the in-flight turn
	state := &interactiveState{agentSession: sess, platform: p, replyCtx: "ctx", turnActive: true, needsRestart: true}
	e.interactiveStates["k"] = state

	msg := &Message{SessionKey: "k", Content: "follow up", ReplyCtx: "ctx", Platform: "test"}
	agent := &resultAgent{session: sess}
	if !e.restartWedgedSession(p, msg, "k", session, agent, sessions, "") {
		t.Fatal("restartWedgedSession() = false for a wedged session")
	}

	// The dying turn releases the lock shortly after the hard stop.
	go func() {
		time.Sleep(200 * time.Millisecond)
		session.Unlock()
	}()

	time.Sleep(900 * time.Millisecond)
	if got := sess.promptCount(); got == 0 {
		t.Fatal("follow-up was not re-dispatched after the restart")
	}
}
