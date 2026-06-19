package core

import (
	"context"
	"strings"
	"testing"
	"time"
)

// cardMarkdown concatenates the markdown content of all CardMarkdown
// elements in the card. Tests in this package use it to assert on the
// rendered text without depending on the public Card API.
func cardMarkdown(c *Card) string {
	if c == nil {
		return ""
	}
	var sb strings.Builder
	for _, el := range c.Elements {
		if md, ok := el.(CardMarkdown); ok {
			sb.WriteString(md.Content)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// modelQueueTestAgent tracks SetModel calls so tests can assert when (and
// whether) the model switch was applied. It also exposes the model name
// as the agent session ID, so the existing controllableAgentSession is
// enough for the event loop.
type modelQueueTestAgent struct {
	stubAgent
	model    string
	setCalls []string
}

func (a *modelQueueTestAgent) SetModel(model string) {
	a.model = model
	a.setCalls = append(a.setCalls, model)
}
func (a *modelQueueTestAgent) GetModel() string { return a.model }
func (a *modelQueueTestAgent) AvailableModels(_ context.Context) []ModelOption {
	return []ModelOption{
		{Name: "gpt-4.1", Desc: "Balanced"},
		{Name: "claude-fable-5", Desc: "Faster"},
	}
}

// TestModelSwitch_QueuedDuringInFlightTurn_AppliesAfterTurnComplete is the
// primary regression test for issue #1303. It pins the new behaviour:
//
//  1. While a turn is in flight, /model must NOT close the session — it
//     must queue the change on the state.
//  2. When the in-flight turn finishes (EventResult{Done:true}), the
//     queued change is applied automatically.
//  3. The reply to the original turn is delivered (not dropped).
func TestModelSwitch_QueuedDuringInFlightTurn_AppliesAfterTurnComplete(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	sess := newControllableSession("mq-inflight")
	mqAgent := &modelQueueTestAgent{}
	e := NewEngine("test", mqAgent, []Platform{p}, "", LangEnglish)
	defer func() { _ = e.Stop() }()

	sessions := e.sessions
	session := sessions.GetOrCreateActive("test:mq:u1")
	session.TryLock()

	state := &interactiveState{
		agentSession: sess,
		platform:     p,
		replyCtx:     "ctx",
		// Mark a turn as in flight. The model switch path checks
		// currentTurnUserMessageTimeMs > lastCompletedUserMessageTimeMs
		// to decide between queueing and applying immediately.
		currentTurnUserMessageTimeMs: 100,
		eventsNeedResync:             false,
	}
	iKey := "test:mq:u1"
	e.interactiveMu.Lock()
	e.interactiveStates[iKey] = state
	e.interactiveMu.Unlock()

	// Sanity: confirm the engine reports "in flight" before we queue.
	state.mu.Lock()
	if !state.isTurnInFlightLocked() {
		t.Fatal("setup: expected turn to be in flight")
	}
	state.mu.Unlock()

	// Step 1: queue a model switch while a turn is in flight. The agent's
	// SetModel must NOT be called yet.
	queuedCard := e.queuePendingModelSwitch(state, iKey, "claude-fable-5")
	if queuedCard == nil {
		t.Fatal("expected a non-nil queued card")
	}
	if md := cardMarkdown(queuedCard); !strings.Contains(md, "claude-fable-5") {
		t.Errorf("queued card markdown should mention the target model, got %q", md)
	}
	state.mu.Lock()
	if state.pendingModel != "claude-fable-5" {
		t.Errorf("state.pendingModel = %q, want claude-fable-5", state.pendingModel)
	}
	state.mu.Unlock()
	if got := len(mqAgent.setCalls); got != 0 {
		t.Errorf("SetModel should not be called while the turn is in flight, got %d calls", got)
	}
	if !sess.Alive() {
		t.Error("queueing the model switch must not close the in-flight session")
	}

	// Step 2: turn finishes (EventResult{Done:true}) — the queued model
	// should be applied.
	go func() {
		sess.events <- Event{Type: EventResult, Content: "all done", Done: true}
	}()

	sendDone := make(chan error, 1)
	sendDone <- nil
	e.processInteractiveEvents(state, session, sessions, iKey, "", time.Now(), nil, sendDone, "ctx")

	// Step 3: assert model was applied and the state was cleared.
	if got := len(mqAgent.setCalls); got != 1 {
		t.Errorf("SetModel should have been called once after turn complete, got %d calls (%v)", got, mqAgent.setCalls)
	} else if mqAgent.setCalls[0] != "claude-fable-5" {
		t.Errorf("SetModel called with %q, want claude-fable-5", mqAgent.setCalls[0])
	}
	state.mu.Lock()
	if state.pendingModel != "" {
		t.Errorf("state.pendingModel = %q after apply, want empty", state.pendingModel)
	}
	state.mu.Unlock()

	// Step 4: assert the original turn's reply was delivered to the platform.
	sent := p.getSent()
	found := false
	for _, s := range sent {
		if strings.Contains(s, "all done") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected platform to receive the original turn's reply, got %v", sent)
	}
}

// TestModelSwitch_NotInFlight_AppliesImmediately ensures we didn't break
// the normal (no in-flight turn) path: /model applies synchronously and
// the previous session is closed as before.
func TestModelSwitch_NotInFlight_AppliesImmediately(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	sess := newControllableSession("mq-noflight")
	mqAgent := &modelQueueTestAgent{}
	e := NewEngine("test", mqAgent, []Platform{p}, "", LangEnglish)
	defer func() { _ = e.Stop() }()

	sessions := e.sessions
	session := sessions.GetOrCreateActive("test:mq-noflight:u1")
	session.TryLock()

	state := &interactiveState{
		agentSession: sess,
		platform:     p,
		replyCtx:     "ctx",
		// Both current and last completed are 0 — no in-flight turn.
		eventsNeedResync: false,
	}
	iKey := "test:mq-noflight:u1"
	e.interactiveMu.Lock()
	e.interactiveStates[iKey] = state
	e.interactiveMu.Unlock()

	state.mu.Lock()
	if state.isTurnInFlightLocked() {
		t.Fatal("setup: expected no in-flight turn")
	}
	state.mu.Unlock()

	// Apply directly via the public path used by /model.
	card := e.handleModelCardAction("claude-fable-5", iKey)
	if card == nil {
		t.Fatal("expected a non-nil result card")
	}
	if got := len(mqAgent.setCalls); got != 1 || mqAgent.setCalls[0] != "claude-fable-5" {
		t.Errorf("SetModel calls = %v, want [claude-fable-5]", mqAgent.setCalls)
	}
}

// TestIsTurnInFlightLocked_TracksWatermarks covers the helper's truth
// table: a turn is in flight iff currentTurnUserMessageTimeMs is greater
// than the last completed watermark.
func TestIsTurnInFlightLocked_TracksWatermarks(t *testing.T) {
	tests := []struct {
		name        string
		currentMs   int64
		completedMs int64
		want        bool
	}{
		{"empty state", 0, 0, false},
		{"only completed set", 0, 100, false},
		{"only current set", 100, 0, true},
		{"equal", 100, 100, false},
		{"current ahead", 100, 50, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &interactiveState{
				currentTurnUserMessageTimeMs:   tc.currentMs,
				lastCompletedUserMessageTimeMs: tc.completedMs,
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if got := s.isTurnInFlightLocked(); got != tc.want {
				t.Errorf("isTurnInFlightLocked() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestModelSwitch_TimeoutFallback_ForceAppliesAndNotices covers the 30s
// safety-net for issue #1303. If the in-flight turn does not finish
// within the timeout, the queued model must be applied AND the user
// must be told that their previous message was interrupted so they can
// resend it. We invoke the timer body directly to avoid waiting 30s.
func TestModelSwitch_TimeoutFallback_ForceAppliesAndNotices(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	sess := newControllableSession("mq-timeout")
	mqAgent := &modelQueueTestAgent{}
	e := NewEngine("test", mqAgent, []Platform{p}, "", LangEnglish)
	defer func() { _ = e.Stop() }()

	sessions := e.sessions
	session := sessions.GetOrCreateActive("test:mq-timeout:u1")
	session.TryLock()

	state := &interactiveState{
		agentSession:                   sess,
		platform:                       p,
		replyCtx:                       "ctx",
		currentTurnUserMessageTimeMs:   100,
		lastCompletedUserMessageTimeMs: 0,
	}
	iKey := "test:mq-timeout:u1"
	e.interactiveMu.Lock()
	e.interactiveStates[iKey] = state
	e.interactiveMu.Unlock()

	// Queue the model.
	e.queuePendingModelSwitch(state, iKey, "claude-fable-5")
	state.mu.Lock()
	if state.pendingModel != "claude-fable-5" {
		t.Fatalf("setup: state.pendingModel = %q, want claude-fable-5", state.pendingModel)
	}
	state.mu.Unlock()

	// Run the timer body directly (skips the 30s sleep).
	e.checkAndForceApplyPendingModelSwitch(iKey, "claude-fable-5")

	// Assert: model was applied.
	if got := len(mqAgent.setCalls); got != 1 || mqAgent.setCalls[0] != "claude-fable-5" {
		t.Errorf("SetModel calls = %v, want [claude-fable-5]", mqAgent.setCalls)
	}
	// Assert: pending marker was cleared.
	state.mu.Lock()
	if state.pendingModel != "" {
		t.Errorf("state.pendingModel = %q after force-apply, want empty", state.pendingModel)
	}
	state.mu.Unlock()
	// Assert: the in-flight session was closed.
	if sess.Alive() {
		t.Error("force-apply must close the in-flight session")
	}
	// Assert: the user received the interrupted notice.
	sent := p.getSent()
	found := false
	for _, s := range sent {
		if strings.Contains(s, "interrupted") || strings.Contains(s, "中断") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected platform to receive an interrupted notice, got %v", sent)
	}
}
