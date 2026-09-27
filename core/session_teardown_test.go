package core

import (
	"testing"
	"time"
)

// Regression coverage for the close/spawn race guard added alongside the
// bounded-teardown fix.
//
// The bug these guard against: /stop starts an asynchronous teardown, and a
// message arriving moments later used to spawn a new agent process straight
// away. Two live processes then shared one agent session ID, reading and
// writing the same transcript and acting on it independently.
//
// beginSessionClose/awaitSessionClose serialise that, and markUnsafeResume
// records the case where the previous process could not be confirmed dead —
// there, losing conversation history is strictly better than two agents
// fighting over one transcript.

func TestAwaitSessionCloseBlocksUntilTeardownFinishes(t *testing.T) {
	e := newTestEngine()
	defer e.cancel()

	const key = "test:chat-1:user-1"
	finish := e.beginSessionClose(key)

	returned := make(chan bool, 1)
	go func() { returned <- e.awaitSessionClose(key) }()

	select {
	case <-returned:
		t.Fatal("awaitSessionClose returned while a teardown was still in flight; " +
			"a spawn here would start a second process on the same agent session")
	case <-time.After(50 * time.Millisecond):
	}

	finish()

	select {
	case ok := <-returned:
		if !ok {
			t.Fatal("awaitSessionClose reported an unclean wait after a normal teardown")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("awaitSessionClose did not return after the teardown finished")
	}
}

func TestAwaitSessionCloseReturnsImmediatelyWhenNoTeardown(t *testing.T) {
	e := newTestEngine()
	defer e.cancel()

	returned := make(chan bool, 1)
	go func() { returned <- e.awaitSessionClose("test:chat-2:user-2") }()

	select {
	case ok := <-returned:
		if !ok {
			t.Fatal("awaitSessionClose reported an unclean wait with no teardown registered")
		}
	case <-time.After(time.Second):
		t.Fatal("awaitSessionClose blocked even though no teardown was registered")
	}
}

// An overlapping teardown replaces the registration; the first one finishing
// must not release a waiter that the second one is still holding.
func TestOverlappingTeardownKeepsSpawnBlocked(t *testing.T) {
	e := newTestEngine()
	defer e.cancel()

	const key = "test:chat-3:user-3"
	finishFirst := e.beginSessionClose(key)
	finishSecond := e.beginSessionClose(key)

	returned := make(chan bool, 1)
	go func() { returned <- e.awaitSessionClose(key) }()

	finishFirst()

	select {
	case <-returned:
		t.Fatal("awaitSessionClose returned once the first teardown finished, " +
			"but an overlapping teardown was still running")
	case <-time.After(50 * time.Millisecond):
	}

	finishSecond()

	select {
	case ok := <-returned:
		if !ok {
			t.Fatal("awaitSessionClose reported an unclean wait after both teardowns finished")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("awaitSessionClose did not return after the overlapping teardown finished")
	}
}

func TestUnsafeResumeIsConsumedExactlyOnce(t *testing.T) {
	e := newTestEngine()
	defer e.cancel()

	const key = "test:chat-4:user-4"
	if e.consumeUnsafeResume(key) {
		t.Fatal("a session key with no failed teardown must not be flagged unsafe to resume")
	}

	e.markUnsafeResume(key)
	if !e.consumeUnsafeResume(key) {
		t.Fatal("after an unconfirmed teardown the next spawn must start a fresh agent " +
			"session instead of resuming the one that may still be live")
	}
	if e.consumeUnsafeResume(key) {
		t.Fatal("the unsafe-resume flag must be cleared once consumed; " +
			"leaving it set would discard conversation history on every later spawn")
	}
}

// A wedged teardown must not outlive the engine: shutdown has to unblock the
// waiter, and the wait must be reported as unclean so the caller does not
// resume the agent session it never confirmed dead.
func TestAwaitSessionCloseGivesUpWhenEngineStops(t *testing.T) {
	e := newTestEngine()

	const key = "test:chat-5:user-5"
	e.beginSessionClose(key) // deliberately never finished

	returned := make(chan bool, 1)
	go func() { returned <- e.awaitSessionClose(key) }()

	e.cancel()

	select {
	case ok := <-returned:
		if ok {
			t.Fatal("awaitSessionClose reported a clean wait even though the engine " +
				"shut down with the teardown still in flight")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("awaitSessionClose ignored engine shutdown and kept waiting")
	}
}

// Sessions without a key (no interactive state) must not deadlock the guard.
func TestSessionCloseGuardIgnoresEmptyKey(t *testing.T) {
	e := newTestEngine()
	defer e.cancel()

	e.beginSessionClose("")() // must be a no-op, not a registration
	if !e.awaitSessionClose("") {
		t.Fatal("awaitSessionClose must not block on an empty session key")
	}
	e.markUnsafeResume("")
	if e.consumeUnsafeResume("") {
		t.Fatal("an empty session key must never be flagged unsafe to resume")
	}
}
