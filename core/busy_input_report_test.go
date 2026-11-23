package core

import (
	"context"
	"testing"
)

type injectionReportingPlatform struct {
	*stubPlatformEngine
	target string
	reply  any
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
