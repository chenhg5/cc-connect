package core

import (
	"sync"
	"testing"
	"time"
)

// bgTaskSession is a controllable session that also reports background tasks.
type bgTaskSession struct {
	*controllableAgentSession
	mu        sync.Mutex
	running   int
	lastEnded time.Time
}

func (s *bgTaskSession) BackgroundTasks() (int, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running, s.lastEnded
}

func (s *bgTaskSession) set(running int, lastEnded time.Time) {
	s.mu.Lock()
	s.running, s.lastEnded = running, lastEnded
	s.mu.Unlock()
}

func newBgIdleFixture(t *testing.T, timeout time.Duration) (*Engine, string, *bgTaskSession, *interactiveState) {
	t.Helper()
	e := newTestEngine()
	e.SetAgentSessionIdleTimeout(timeout)
	key := "test:user1"
	sess := &bgTaskSession{controllableAgentSession: newControllableSession("s1")}
	state := &interactiveState{agentSession: sess}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()
	return e, key, sess, state
}

// A running background task keeps the session open past the idle timeout;
// once it ends, the full timeout counts again from that moment.
func TestAgentSessionIdleTimeout_DeferredWhileBackgroundTaskRuns(t *testing.T) {
	const timeout = 40 * time.Millisecond
	e, key, sess, state := newBgIdleFixture(t, timeout)
	sess.set(1, time.Time{})

	e.scheduleAgentSessionIdleClose(key, state)
	select {
	case <-sess.closed:
		t.Fatal("session closed while a background task was running")
	case <-time.After(4 * timeout):
	}

	ended := time.Now()
	sess.set(0, ended)
	select {
	case <-sess.closed:
		if d := time.Since(ended); d < timeout {
			t.Fatalf("closed %v after the task ended, want >= %v", d, timeout)
		}
	case <-time.After(10 * timeout):
		t.Fatal("session was not closed after the background task ended")
	}
	waitForInteractiveStateRemoved(t, e, key)
}

// A task that just ended (count already 0) still defers the close to a full
// timeout after it ended.
func TestAgentSessionIdleTimeout_RestartsAfterLastTaskEnded(t *testing.T) {
	const timeout = 60 * time.Millisecond
	e, key, sess, state := newBgIdleFixture(t, timeout)
	e.scheduleAgentSessionIdleClose(key, state)

	time.Sleep(timeout / 2)
	ended := time.Now()
	sess.set(0, ended)

	select {
	case <-sess.closed:
		if d := time.Since(ended); d < timeout {
			t.Fatalf("closed %v after the task ended, want >= %v", d, timeout)
		}
	case <-time.After(10 * timeout):
		t.Fatal("session was not closed")
	}
	waitForInteractiveStateRemoved(t, e, key)
}

// A foreground turn cancels a deferred idle timer like any other.
func TestAgentSessionIdleTimeout_DeferredTimerIsCancellable(t *testing.T) {
	const timeout = 20 * time.Millisecond
	e, key, sess, state := newBgIdleFixture(t, timeout)
	sess.set(1, time.Time{})
	e.scheduleAgentSessionIdleClose(key, state)
	time.Sleep(3 * timeout) // at least one deferral has re-armed the timer

	e.cancelAgentSessionIdleClose(state)
	sess.set(0, time.Now().Add(-time.Hour))
	select {
	case <-sess.closed:
		t.Fatal("deferred idle timer closed the session after cancel")
	case <-time.After(5 * timeout):
	}
}

// Sessions without background-task reporting keep the old behaviour.
func TestAgentSessionIdleDeferral_NoReporter(t *testing.T) {
	if wait, _ := agentSessionIdleDeferral(newControllableSession("s"), time.Minute); wait != 0 {
		t.Fatalf("wait = %v, want 0", wait)
	}
}
