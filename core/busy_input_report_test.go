package core

import (
	"context"
	"testing"
	"time"
)

type injectionReportingPlatform struct {
	*stubPlatformEngine
	target  string
	reply   any
	dropped map[any]string
}

func (p *injectionReportingPlatform) ReportInjected(_ context.Context, reply any, target string) {
	p.reply, p.target = reply, target
}

func TestBusyInputReportsOnlySuccessfulInjectionWithOriginalTurn(t *testing.T) {
	for _, capable := range []bool{true, false} {
		t.Run(map[bool]string{true: "injected", false: "queued"}[capable], func(t *testing.T) {
			sess := &busySteeringSession{queuingAgentSession: newQueuingSession("report-input"), steerOK: capable}
			e, p, state, _ := setupBusySession(t, sess)
			state.currentMessageID = "original-turn"
			reporter := &injectionReportingPlatform{stubPlatformEngine: p}
			msg := busyMessage("also test errors")
			e.ReceiveMessage(reporter, msg)
			if capable {
				if reporter.target != "original-turn" || reporter.reply != msg.ReplyCtx || pendingCount(state) != 0 {
					t.Fatalf("injection receipt: target=%q reply=%v queue=%d", reporter.target, reporter.reply, pendingCount(state))
				}
			} else if reporter.target != "" || pendingCount(state) != 1 {
				t.Fatalf("queued input misreported as injected: target=%q queue=%d", reporter.target, pendingCount(state))
			}
		})
	}
}

func (p *injectionReportingPlatform) ReportDropped(_ context.Context, reply any, reason string) {
	if p.dropped == nil {
		p.dropped = map[any]string{}
	}
	p.dropped[reply] = reason
}
func TestStopAndOverflowAccountForQueuedInputs(t *testing.T) {
	sess := &busySteeringSession{queuingAgentSession: newQueuingSession("stop-input"), steerOK: true}
	e, base, state, session := setupBusySession(t, sess)
	state.currentMessageID = "root"

	state.busySession = session
	p := &injectionReportingPlatform{stubPlatformEngine: base}
	msg := busyMessage("attachment supplement")
	msg.MessageID = "supplement"
	msg.ReplyCtx = "supplement"
	msg.Files = []FileAttachment{{FileName: "notes.txt", Data: []byte("notes")}}
	e.ReceiveMessage(p, msg)
	if pendingCount(state) != 1 {
		t.Fatal("expected attachment queued")
	}
	e.maxQueuedMessages = 1
	overflow := busyMessage("overflow")
	overflow.ReplyCtx = "overflow"
	e.ReceiveMessage(p, overflow)
	if p.dropped["overflow"] != "rejected" {
		t.Fatalf("overflow receipt=%v", p.dropped)
	}
	status := e.stopInteractiveSessionWithOptions("test:user1", false)
	if !status || pendingCount(state) != 0 || p.dropped["supplement"] != "cancelled" {
		t.Fatalf("stop=%v queue=%d receipts=%v", status, pendingCount(state), p.dropped)
	}
}

type staleDrainReportingPlatform struct {
	*stubPlatformEngine
	t     *testing.T
	state *interactiveState
	drops map[any][]string
}

func (p *staleDrainReportingPlatform) ReportDropped(_ context.Context, reply any, reason string) {
	if !p.state.mu.TryLock() {
		p.t.Error("drop receipt must run outside the interactive-state lock")
	} else {
		p.state.mu.Unlock()
	}
	p.drops[reply] = append(p.drops[reply], reason)
}

func TestQueuedStaleInputReceiptsOnDrain(t *testing.T) {
	for _, tc := range []struct {
		name       string
		completed  bool
		freshInput bool
	}{
		{name: "pending drain"},
		{name: "completed turn", completed: true},
		{name: "pending drain with fresh input", freshInput: true},
		{name: "completed turn with fresh input", completed: true, freshInput: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const key = "test:user1"
			as := newResultAgentSession("fresh input complete")
			e, base, state, session := setupBusySession(t, as)
			p := &staleDrainReportingPlatform{
				stubPlatformEngine: base, t: t, state: state, drops: make(map[any][]string),
			}
			for i, id := range []string{"old-first", "old-second"} {
				msg := busyMessage("queued")
				msg.MessageID, msg.ReplyCtx, msg.UserMessageTimeMs = id, id, int64(1_000+i)
				if !e.queueMessageForBusySession(p, msg, key) {
					t.Fatalf("queue %s failed", id)
				}
			}
			if len(p.drops) != 0 || pendingCount(state) != 2 {
				t.Fatal("inputs must first be accepted into the queue")
			}
			if tc.freshInput {
				msg := busyMessage("fresh")
				msg.MessageID, msg.ReplyCtx, msg.UserMessageTimeMs = "fresh", "fresh", 3_000
				if !e.queueMessageForBusySession(p, msg, key) {
					t.Fatal("fresh input could not be queued")
				}
			}
			state.mu.Lock()
			state.currentTurnUserMessageTimeMs = 2_000
			state.mu.Unlock()
			if tc.completed {
				as.events <- Event{Type: EventResult, Done: true, Content: "turn complete"}
				e.processInteractiveEvents(state, session, e.sessions, key, "current", time.Now(), nil, nil, nil, 0)
			}
			// In the completed-turn case, the explicit drain has no items left
			// and must not report another receipt.
			if !e.drainPendingMessages(state, session, e.sessions, key, 0) {
				t.Fatal("drain must release the empty session")
			}
			for _, id := range []string{"old-first", "old-second"} {
				got := p.drops[id]
				if len(got) != 1 || got[0] != "stale" {
					t.Errorf("receipts for %s = %v, want [stale]", id, got)
				}
			}
			if len(p.drops) != 2 || pendingCount(state) != 0 {
				t.Fatalf("unexpected receipts or pending input: receipts=%v pending=%d", p.drops, pendingCount(state))
			}
			wantSends := 0
			if tc.freshInput {
				wantSends = 1
			}
			if len(as.sentPrompts) != wantSends || (tc.freshInput && as.sentPrompts[0] != "fresh") {
				t.Fatalf("sent prompts = %v, want %d fresh sends", as.sentPrompts, wantSends)
			}
		})
	}
}
