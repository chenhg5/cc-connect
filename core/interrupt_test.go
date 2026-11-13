package core

import (
	"sync"
	"testing"
)

// interruptSpySession wraps the standard stub session and records interrupts.
type interruptSpySession struct {
	*resultAgentSession
	mu         sync.Mutex
	interrupts int
	reason     string
}

func (s *interruptSpySession) Interrupt(reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.interrupts++
	s.reason = reason
	return nil
}

func (s *interruptSpySession) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.interrupts
}

func newInterruptTestEngine(t *testing.T) *Engine {
	t.Helper()
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangChinese)
	t.Cleanup(e.cancel)
	return e
}

// A running turn must be interruptible without tearing the session down.
func TestInterruptInteractiveTurn_SetsCancelFlag(t *testing.T) {
	e := newInterruptTestEngine(t)
	sess := &interruptSpySession{resultAgentSession: newResultAgentSession("ok")}
	state := &interactiveState{agentSession: sess, turnActive: true}
	e.interactiveStates["k"] = state

	if !e.interruptInteractiveTurn("k", "user /stop", true, true) {
		t.Fatal("interruptInteractiveTurn() = false, want true for interruptible session")
	}
	if got := sess.count(); got != 1 {
		t.Fatalf("Interrupt calls = %d, want 1", got)
	}
	if _, ok := e.interactiveStates["k"]; !ok {
		t.Fatal("state was removed; interrupt must keep the session alive")
	}
	state.mu.Lock()
	flagged := state.interruptedByUser
	state.mu.Unlock()
	if !flagged {
		t.Fatal("interruptedByUser = false, want true so the result is treated as a user cancel")
	}
}

// Idle sessions and agents without interrupt support must fall back to a hard stop.
func TestInterruptInteractiveTurn_RejectsIdleAndUnsupported(t *testing.T) {
	e := newInterruptTestEngine(t)

	idle := &interactiveState{agentSession: &interruptSpySession{resultAgentSession: newResultAgentSession("ok")}}
	e.interactiveStates["idle"] = idle
	if e.interruptInteractiveTurn("idle", "x", true, true) {
		t.Fatal("interruptInteractiveTurn() = true for an idle session, want false")
	}

	unsupported := &interactiveState{agentSession: newResultAgentSession("ok"), turnActive: true}
	e.interactiveStates["plain"] = unsupported
	if e.interruptInteractiveTurn("plain", "x", true, true) {
		t.Fatal("interruptInteractiveTurn() = true for a session without Interrupt(), want false")
	}

	if e.interruptInteractiveTurn("missing", "x", true, true) {
		t.Fatal("interruptInteractiveTurn() = true for an unknown session, want false")
	}
}


// stop_wait_secs = 0 must end the session immediately instead of sending an
// interrupt and walking away.
func TestStopWithZeroWaitEndsSession(t *testing.T) {
	old := suppressForceStopAfterInterrupt
	suppressForceStopAfterInterrupt = false
	t.Cleanup(func() { suppressForceStopAfterInterrupt = old })

	e := newInterruptTestEngine(t)
	e.SetStopWait(0)
	sess := &interruptSpySession{resultAgentSession: newResultAgentSession("ok")}
	state := &interactiveState{agentSession: sess, turnActive: true}
	e.interactiveStates["k"] = state

	if !e.interruptInteractiveTurn("k", "user /stop", true, true) {
		t.Fatal("interruptInteractiveTurn() = false")
	}
	if _, ok := e.interactiveStates["k"]; ok {
		t.Fatal("state still present; zero wait must hard-stop the session")
	}
}
