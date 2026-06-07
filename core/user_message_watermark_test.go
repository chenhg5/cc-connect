package core

import "testing"

func TestIsStaleUserMessageLocked(t *testing.T) {
	e := &Engine{}
	s := &interactiveState{lastCompletedUserMessageTimeMs: 200}
	if !e.isStaleUserMessageLocked(s, 199) {
		t.Fatal("199 should be stale vs 200")
	}
	if e.isStaleUserMessageLocked(s, 200) {
		t.Fatal("200 should not be stale vs 200 (strict <)")
	}
	if e.isStaleUserMessageLocked(s, 201) {
		t.Fatal("201 should not be stale")
	}
	if e.isStaleUserMessageLocked(s, 0) {
		t.Fatal("unset time never stale")
	}
	s.lastCompletedUserMessageTimeMs = 0
	if e.isStaleUserMessageLocked(s, 50) {
		t.Fatal("no watermark means not stale")
	}

	// In-flight turn (B processing) without completed watermark yet.
	s = &interactiveState{currentTurnUserMessageTimeMs: 500}
	if !e.isStaleUserMessageLocked(s, 499) {
		t.Fatal("499 should be stale vs in-flight 500")
	}
	if e.isStaleUserMessageLocked(s, 500) {
		t.Fatal("500 should not be stale vs in-flight 500")
	}

	// Queued newer message while session is busy.
	s = &interactiveState{
		currentTurnUserMessageTimeMs: 400,
		pendingMessages: []queuedMessage{
			{userMessageTimeMs: 600},
		},
	}
	if !e.isStaleUserMessageLocked(s, 599) {
		t.Fatal("599 should be stale vs queued 600")
	}
	if e.isStaleUserMessageLocked(s, 600) {
		t.Fatal("600 should not be stale vs queued 600")
	}
}

func TestNoteUserTurnCompleted(t *testing.T) {
	e := &Engine{}
	s := &interactiveState{currentTurnUserMessageTimeMs: 100, lastCompletedUserMessageTimeMs: 0}
	e.noteUserTurnCompleted(s)
	if s.lastCompletedUserMessageTimeMs != 100 {
		t.Fatalf("last = %d", s.lastCompletedUserMessageTimeMs)
	}
	s.currentTurnUserMessageTimeMs = 90
	e.noteUserTurnCompleted(s)
	if s.lastCompletedUserMessageTimeMs != 100 {
		t.Fatalf("older turn should not lower last, got %d", s.lastCompletedUserMessageTimeMs)
	}
	s.currentTurnUserMessageTimeMs = 150
	e.noteUserTurnCompleted(s)
	if s.lastCompletedUserMessageTimeMs != 150 {
		t.Fatalf("last = %d", s.lastCompletedUserMessageTimeMs)
	}
}

func TestDiscardStaleUserMessageIfNeeded(t *testing.T) {
	e := newTestEngine()
	sessionKey := "feishu:oc_chat:ou_user"
	e.interactiveMu.Lock()
	e.interactiveStates[sessionKey] = &interactiveState{
		lastCompletedUserMessageTimeMs: 2_000,
	}
	e.interactiveMu.Unlock()

	stale := &Message{
		SessionKey:        sessionKey,
		Platform:          "feishu",
		MessageID:         "om_redelivery",
		UserMessageTimeMs: 1_000,
		Content:           "old task",
	}
	if !e.discardStaleUserMessageIfNeeded(sessionKey, stale) {
		t.Fatal("expected redelivered older create_time to be discarded")
	}

	fresh := &Message{
		SessionKey:        sessionKey,
		Platform:          "feishu",
		MessageID:         "om_new",
		UserMessageTimeMs: 2_500,
		Content:           "new task",
	}
	if e.discardStaleUserMessageIfNeeded(sessionKey, fresh) {
		t.Fatal("message newer than watermark must not be discarded")
	}

	if e.discardStaleUserMessageIfNeeded(sessionKey, &Message{
		SessionKey: sessionKey, Content: "no timestamp",
	}) {
		t.Fatal("zero UserMessageTimeMs must not be discarded")
	}

	// B in-flight: A redelivery must drop even before EventResult.
	e.interactiveMu.Lock()
	e.interactiveStates[sessionKey] = &interactiveState{
		currentTurnUserMessageTimeMs: 5_000,
	}
	e.interactiveMu.Unlock()
	if !e.discardStaleUserMessageIfNeeded(sessionKey, &Message{
		SessionKey:        sessionKey,
		UserMessageTimeMs: 4_000,
		MessageID:         "om_a_redelivery",
	}) {
		t.Fatal("A redelivery should drop while B is in-flight")
	}
}

func TestNoteUserMessageAccepted(t *testing.T) {
	e := newTestEngine()
	sessionKey := "feishu:oc_chat:ou_user"
	e.interactiveMu.Lock()
	e.interactiveStates[sessionKey] = &interactiveState{}
	e.interactiveMu.Unlock()

	e.noteUserMessageAccepted(sessionKey, 3_000)
	e.interactiveMu.Lock()
	state := e.interactiveStates[sessionKey]
	e.interactiveMu.Unlock()
	state.mu.Lock()
	got := state.currentTurnUserMessageTimeMs
	state.mu.Unlock()
	if got != 3_000 {
		t.Fatalf("currentTurn = %d, want 3000", got)
	}

	e.noteUserMessageAccepted(sessionKey, 2_000)
	state.mu.Lock()
	got = state.currentTurnUserMessageTimeMs
	state.mu.Unlock()
	if got != 3_000 {
		t.Fatalf("older accept must not lower watermark, got %d", got)
	}
}

func TestQueueMessageForBusySession_RejectsStaleBeforeEnqueue(t *testing.T) {
	e := newTestEngine()
	e.maxQueuedMessages = 5
	sessionKey := "feishu:oc_chat:ou_user"
	p := &stubPlatformEngine{n: "feishu"}

	e.interactiveMu.Lock()
	e.interactiveStates[sessionKey] = &interactiveState{
		platform:                       p,
		lastCompletedUserMessageTimeMs: 5_000,
	}
	e.interactiveMu.Unlock()

	msg := &Message{
		SessionKey:        sessionKey,
		Platform:          "feishu",
		MessageID:         "om_stale_queue",
		UserMessageTimeMs: 3_000,
		Content:           "stale while busy",
		ReplyCtx:          "rctx",
	}
	if !e.queueMessageForBusySession(p, msg, sessionKey) {
		t.Fatal("stale message should be handled (dropped) without enqueue failure")
	}

	e.interactiveMu.Lock()
	state := e.interactiveStates[sessionKey]
	e.interactiveMu.Unlock()
	state.mu.Lock()
	if len(state.pendingMessages) != 0 {
		t.Fatalf("pending = %d, want 0 for stale message", len(state.pendingMessages))
	}

	// B in-flight (not completed): A redelivery must not enter the queue.
	state.currentTurnUserMessageTimeMs = 8_000
	state.lastCompletedUserMessageTimeMs = 0
	state.pendingMessages = nil
	state.mu.Unlock()

	msg = &Message{
		SessionKey:        sessionKey,
		Platform:          "feishu",
		MessageID:         "om_a_redelivery",
		UserMessageTimeMs: 7_000,
		Content:           "A while B processing",
		ReplyCtx:          "rctx2",
	}
	if !e.queueMessageForBusySession(p, msg, sessionKey) {
		t.Fatal("stale vs in-flight should return handled=true")
	}
	state.mu.Lock()
	if len(state.pendingMessages) != 0 {
		t.Fatalf("pending = %d, want 0 for stale vs in-flight", len(state.pendingMessages))
	}
	state.mu.Unlock()
}
