package pi

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// A derived timeout context consults its parent's deadline. Observe that
// boundary so a fixed ordinary-prompt ACK deadline is caught immediately,
// without making a regression test wait thirty seconds or run a model.
type rpcSessionDeadlineProbe struct {
	context.Context
	deadlineReads atomic.Int32
}

func (c *rpcSessionDeadlineProbe) Deadline() (time.Time, bool) {
	c.deadlineReads.Add(1)
	return c.Context.Deadline()
}

func TestSharedLifecycle_NormalPromptWaitsThroughPreflightCompaction(t *testing.T) {
	s, wire := issue2161UnitSession(t)
	probe := &rpcSessionDeadlineProbe{Context: s.ctx}
	s.ctx = probe
	command, done := issue2161UnitStartSend(t, s, wire, "work after compaction")
	if probe.deadlineReads.Load() != 0 {
		t.Fatal("ordinary prompt added an ACK deadline before Pi's preflight work completed")
	}
	// Source-derived Pi ordering: preflight compaction can precede both the
	// prompt ACK and agent_start. Neither compaction event releases Send.
	s.handleEvent(map[string]any{"type": "compaction_start", "reason": "threshold"})
	select {
	case err := <-done:
		t.Fatalf("Send finished during preflight compaction: %v", err)
	case <-time.After(20 * time.Millisecond): // harness observation only
	}
	issue2161UnitNoTerminal(t, s, "preflight compaction before prompt ACK")
	if !s.Alive() || s.ctx.Err() != nil {
		t.Fatal("unacknowledged preflight work retired the session")
	}
	s.handleEvent(map[string]any{"type": "compaction_end", "reason": "threshold", "willRetry": false})
	select {
	case err := <-done:
		t.Fatalf("compaction end completed an unacknowledged prompt: %v", err)
	default:
	}
	issue2161UnitNoTerminal(t, s, "compaction end before prompt ACK")
	s.handleEvent(issue2161UnitResponse(command, true, "started"))
	issue2161UnitWaitSend(t, done, "")
	s.handleEvent(map[string]any{"type": "agent_start"})
	s.handleEvent(issue2161UnitText("real answer"))
	s.handleEvent(map[string]any{"type": "agent_end", "willRetry": false})
	events := issue2161UnitNoTerminal(t, s, "agent_end after delayed ACK")
	if len(events) != 1 || events[0].Type != core.EventText || events[0].Content != "real answer" {
		t.Fatalf("delayed ACK lost the original reply: %+v", events)
	}
	s.handleEvent(map[string]any{"type": "agent_settled"})
	if events := drainEvents(s); len(events) != 1 || events[0].Type != core.EventResult || !events[0].Done {
		t.Fatalf("delayed ordinary ACK did not produce one settled terminal: %+v", events)
	}
}

func TestSharedLifecycle_PreflightACKWaitFollowsSessionCancellation(t *testing.T) {
	s, wire := issue2161UnitSession(t)
	_, done := issue2161UnitStartSend(t, s, wire, "work")
	s.handleEvent(map[string]any{"type": "compaction_start", "reason": "threshold"})
	issue2161UnitNoTerminal(t, s, "preflight before session cancellation")
	s.cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("sent prompt cancellation must remain uncertain, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("session cancellation did not release ordinary ACK waiter")
	}
}
