package core

import (
	"testing"
	"time"
)

func TestOutboxPushAfterCloseRunsOutOfBand(t *testing.T) {
	prev := outboxInline
	outboxInline = false
	t.Cleanup(func() { outboxInline = prev })

	o := newOutbox()
	o.close()

	done := make(chan struct{}, 1)
	o.push(func() { done <- struct{}{} })
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("task pushed after close was lost")
	}
}

func TestSweepIdleOutboxesRetiresOnlyIdleSessions(t *testing.T) {
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, nil, "", LangEnglish)
	t.Cleanup(e.cancel)

	idle := e.outboxFor("idle")
	fresh := e.outboxFor("fresh")

	oldTimeout := outboxIdleTimeout
	outboxIdleTimeout = 10 * time.Minute
	t.Cleanup(func() { outboxIdleTimeout = oldTimeout })

	idle.lastUsed.Store(time.Now().Add(-11 * time.Minute).UnixNano())
	e.sweepIdleOutboxes(time.Now())

	e.outboxMu.Lock()
	_, idleExists := e.outboxes["idle"]
	freshGot, freshExists := e.outboxes["fresh"]
	e.outboxMu.Unlock()
	if idleExists {
		t.Fatal("idle outbox was not retired")
	}
	if !freshExists || freshGot != fresh {
		t.Fatal("fresh outbox was retired or replaced")
	}
	if got := e.outboxFor("idle"); got == idle {
		t.Fatal("outboxFor(idle) returned the retired outbox")
	}
}
