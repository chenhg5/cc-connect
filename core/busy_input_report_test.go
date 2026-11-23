package core

import (
	"context"
	"testing"
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
