package core

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestBusySteer_AcceptancePreservesMessageBookkeeping(t *testing.T) {
	as := newAckSteerAgentSession(SteerQueued)
	e, p, key, session := setupBusySession(t, as)
	e.SetBusyMessageMode("steer")
	e.SetInjectSender(true)
	defer session.Unlock(0)
	old := session.GetLastUserActivity()
	accepted := 0
	e.ReceiveMessage(p, &Message{SessionKey: key, Content: "supplement", UserID: "u2", UserName: "Second User", Platform: "test", UserMessageTimeMs: 2000, OnAccepted: func() { accepted++ }})
	waitBusyCondition(t, func() bool { return len(session.GetHistory(0)) == 1 })
	if accepted != 1 || !session.GetLastUserActivity().After(old) {
		t.Fatalf("accepted=%d, activity did not advance=%t", accepted, !session.GetLastUserActivity().After(old))
	}
	history := session.GetHistory(0)
	if len(history) != 1 || history[0].Content != "supplement" {
		t.Fatalf("missing supplement in history: %+v", history)
	}
	as.steerMu.Lock()
	defer as.steerMu.Unlock()
	if !strings.Contains(as.lastText, "sender_id=u2") {
		t.Fatalf("missing sender identity: %q", as.lastText)
	}
}

func TestBusySteer_DefiniteRejectionFallsBackToQueue(t *testing.T) {
	as := newAckSteerAgentSession(SteerQueued)
	as.sendErr = &SteerNotAcceptedError{Err: errors.New("native compaction rejected input")}
	e, p, key, session := setupBusySession(t, as)
	e.SetBusyMessageMode("steer")
	defer session.Unlock(0)
	e.ReceiveMessage(p, &Message{SessionKey: key, Content: "keep this correction"})
	waitBusyCondition(t, func() bool {
		for _, text := range p.getSent() {
			if text == e.i18n.T(MsgSteerFallbackQueue) {
				return true
			}
		}
		return false
	})
	if queuedCountForTest(e, key) != 1 {
		t.Fatal("definitely rejected correction was not queued")
	}
	if sent := p.getSent(); len(sent) != 2 || sent[1] != e.i18n.T(MsgSteerFallbackQueue) {
		t.Fatalf("expected exactly one fallback receipt, got %v", sent)
	}
}

func TestBusySteer_StoppedStateDoesNotSend(t *testing.T) {
	as := newAckSteerAgentSession(SteerQueued)
	e, p, key, session := setupBusySession(t, as)
	e.SetBusyMessageMode("steer")
	defer session.Unlock(0)
	e.interactiveStates[key].markStopped()
	e.ReceiveMessage(p, &Message{SessionKey: key, Content: "must not reach stopped task"})
	if as.steerCalls != 0 {
		t.Fatal("sent input after session was stopped")
	}
}

func TestBusySteer_ReloadConcurrentWithDispatch(t *testing.T) {
	as := newAckSteerAgentSession(SteerQueued)
	e, p, key, session := setupBusySession(t, as)
	defer session.Unlock(0)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 100 {
			e.SetBusyMessageMode("STEER")
			e.SetBusyMessageMode("queue")
		}
	}()
	for range 100 {
		e.ReceiveMessage(p, &Message{SessionKey: key, Content: "concurrent correction"})
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reload blocked")
	}
}

func TestBusySteer_NoCancellableSenderQueues(t *testing.T) {
	as := newBusySteerAgentSession("", true)
	e, p, key, session := setupBusySession(t, as)
	e.SetBusyMessageMode("steer")
	defer session.Unlock(0)
	e.ReceiveMessage(p, &Message{SessionKey: key, Content: "safe fallback"})
	if as.sendCount != 0 || queuedCountForTest(e, key) != 1 {
		t.Fatal("generic Send must not be treated as cancellable native steering")
	}
}

func TestBusySteer_ClosedOrIdleAdmissionUsesQueueReceipt(t *testing.T) {
	for _, phase := range []string{"handoff", "closed", "idle"} {
		t.Run(phase, func(t *testing.T) {
			as := newAckSteerAgentSession(SteerQueued)
			e, p, key, session := setupBusySession(t, as)
			e.SetBusyMessageMode("steer")
			defer session.Unlock(0)
			state := e.interactiveStates[key]
			state.steerHandoff = phase == "handoff"
			state.steerAdmissionClosed = phase == "closed"
			if phase == "idle" {
				session.Unlock(0)
			}
			if !e.enqueueBusyInput(p, &Message{SessionKey: key, Content: "next task"}, key, true, MsgSteerFallbackQueue, session, e.sessions) {
				t.Fatal("input was not queued")
			}
			waitBusyCondition(t, func() bool { return len(p.getSent()) > 0 })
			if replies := p.getSent(); len(replies) != 1 || replies[0] != e.i18n.T(MsgSteerFallbackQueue) {
				t.Fatalf("%s receipt = %v", phase, replies)
			}
			state.mu.Lock()
			steerable := state.pendingMessages[0].steerWhenReady
			state.mu.Unlock()
			if steerable {
				t.Fatalf("%s input remains steerable", phase)
			}
		})
	}
}

func TestBusySteer_ClosedAdmissionResolvesWaitingReceiptOnce(t *testing.T) {
	as := newAckSteerAgentSession(SteerQueued)
	e, p, key, session := setupBusySession(t, as)
	defer session.Unlock(0)
	receipt := make(chan struct{})
	state := e.interactiveStates[key]
	state.pendingMessages = []queuedMessage{{platform: p, replyCtx: "waiting", content: "next task", steerWhenReady: true, receiptSent: receipt}}
	e.finishBusySteerAdmission(state)
	e.finishBusySteerAdmission(state)
	if replies := p.getSent(); len(replies) != 0 {
		t.Fatalf("fallback overtook waiting receipt: %v", replies)
	}
	close(receipt)
	waitBusyCondition(t, func() bool { return len(p.getSent()) > 0 })
	if replies := p.getSent(); len(replies) != 1 || replies[0] != e.i18n.T(MsgSteerFallbackQueue) {
		t.Fatalf("fallback receipts = %v", replies)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.pendingMessages[0].steerWhenReady {
		t.Fatal("ordinary followup remains steerable")
	}
}
