package core

import (
	"context"
	"testing"
	"time"
)

func TestUnsolicitedReaderRecoversAfterStopTimeout(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	as := newControllableSession("reader-recovery")
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	key := "test:reader-recovery"
	session := e.sessions.GetOrCreateActive(key)
	state := &interactiveState{agentSession: as, platform: p, replyCtx: "ctx"}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()
	defer func() { e.cleanupInteractiveState(key, state) }()

	_, cancel := context.WithCancel(context.Background())
	stuckDone := make(chan struct{})
	state.mu.Lock()
	state.unsolicitedCancel = cancel
	state.unsolicitedDone = stuckDone
	state.mu.Unlock()
	e.stopUnsolicitedReader(state) // waits the bounded 5-second stop deadline
	state.mu.Lock()
	needsResync := state.eventsNeedResync
	state.mu.Unlock()
	if !needsResync {
		t.Fatal("stop timeout did not mark the event stream for resync")
	}
	gen, ok := session.TryLock()
	if !ok {
		t.Fatal("could not hold foreground lock")
	}
	e.scheduleUnsolicitedReaderRecovery(state, session, e.sessions, key, "")
	close(stuckDone) // old reader finally exits while foreground still owns the lock
	time.Sleep(20 * time.Millisecond)
	state.mu.Lock()
	readerStartedEarly := state.unsolicitedCancel != nil
	state.mu.Unlock()
	if readerStartedEarly {
		t.Fatal("background reader restarted before foreground lock was released")
	}
	session.Unlock(gen)
	deadline := time.After(3 * time.Second)
	for {
		state.mu.Lock()
		ready := state.unsolicitedCancel != nil && !state.eventsNeedResync
		state.mu.Unlock()
		if ready {
			break
		}
		select {
		case <-deadline:
			t.Fatal("background reader never recovered after timed-out reader exited")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	as.events <- Event{Type: EventResult, Content: "late background answer", Done: true}
	for {
		for _, sent := range p.getSent() {
			if sent == "late background answer" {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatalf("recovered reader did not deliver background event: %v", p.getSent())
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}
