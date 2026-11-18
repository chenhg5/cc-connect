package core

import (
	"strings"
	"testing"
	"time"
)

// setupQueueNoticeSession installs sess as the live agent for "test:user1"
// and locks the session as if a turn were in flight.
func setupQueueNoticeSession(t *testing.T, sess AgentSession) (*Engine, *stubPlatformEngine, *interactiveState, *Session) {
	t.Helper()
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	key := "test:user1"
	state := &interactiveState{agentSession: sess, platform: p, replyCtx: "turn-ctx"}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()
	session := e.sessions.GetOrCreateActive(key)
	if _, ok := session.TryLock(); !ok {
		t.Fatal("expected to lock session for busy setup")
	}
	t.Cleanup(func() { session.Unlock(0) })
	return e, p, state, session
}

func queueNoticeMessage(content string, at int64) *Message {
	return &Message{
		SessionKey:        "test:user1",
		Platform:          "test",
		MessageID:         "msg-" + content,
		Content:           content,
		ReplyCtx:          "busy-ctx",
		UserMessageTimeMs: at,
	}
}

func queueNoticePending(state *interactiveState) int {
	state.mu.Lock()
	defer state.mu.Unlock()
	return len(state.pendingMessages)
}

func queueNoticeSent(sent []string, want string) bool {
	for _, s := range sent {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}

// queue_notice = false: messages that arrive while a turn runs wait for it
// without the "will process after the current task" reply, and are then sent
// to the agent once each, in the order they arrived.
func TestQueueNoticeOff_QueuesSilentlyThenDrainsInOrderOnce(t *testing.T) {
	sess := newQueuingSession("silent")
	e, p, state, session := setupQueueNoticeSession(t, sess)
	e.SetQueueNotice(false)

	for i, content := range []string{"first", "second", "third"} {
		e.ReceiveMessage(p, queueNoticeMessage(content, int64(1_000+i)))
	}
	if got := queueNoticePending(state); got != 3 {
		t.Fatalf("pending messages = %d, want 3", got)
	}
	if sent := p.getSent(); queueNoticeSent(sent, e.i18n.T(MsgMessageQueued)) {
		t.Fatalf("sent = %v, want no MsgMessageQueued", sent)
	}

	waitSends := func(n int) bool {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			sess.sendMu.Lock()
			got := len(sess.sendCalls)
			sess.sendMu.Unlock()
			if got >= n {
				return true
			}
			time.Sleep(5 * time.Millisecond)
		}
		return false
	}
	go func() {
		sess.events <- Event{Type: EventResult, Content: "answer0", Done: true}
		for n := 1; n <= 3; n++ {
			if !waitSends(n) {
				return
			}
			sess.events <- Event{Type: EventResult, Content: "answer", Done: true}
		}
	}()
	sendDone := make(chan error, 1)
	sendDone <- nil
	done := make(chan struct{})
	go func() {
		e.processInteractiveEvents(state, session, e.sessions, "test:user1", "msg0", time.Now(), nil, sendDone, "turn-ctx", 0)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("processInteractiveEvents did not drain the queue in time")
	}

	sess.sendMu.Lock()
	calls := append([]string(nil), sess.sendCalls...)
	sess.sendMu.Unlock()
	if len(calls) != 3 {
		t.Fatalf("agent sends = %d, want each queued message once: %v", len(calls), calls)
	}
	for i, want := range []string{"first", "second", "third"} {
		if !strings.Contains(calls[i], want) {
			t.Fatalf("agent sends = %v, want first, second, third in order", calls)
		}
	}
	if got := queueNoticePending(state); got != 0 {
		t.Fatalf("pending messages after drain = %d, want 0", got)
	}
	if queueNoticeSent(p.getSent(), e.i18n.T(MsgMessageQueued)) {
		t.Fatalf("sent = %v, want no MsgMessageQueued", p.getSent())
	}
}

// A full queue still says so with queue_notice = false: that message is not
// kept, and silence would lose it without a word.
func TestQueueNoticeOff_FullQueueIsStillReported(t *testing.T) {
	e, p, state, _ := setupQueueNoticeSession(t, newQueuingSession("full"))
	e.SetQueueNotice(false)
	e.maxQueuedMessages = 1
	e.ReceiveMessage(p, queueNoticeMessage("kept", 1_000))
	e.ReceiveMessage(p, queueNoticeMessage("over", 1_001))
	if got := queueNoticePending(state); got != 1 {
		t.Fatalf("pending messages = %d, want 1", got)
	}
	sent := p.getSent()
	if queueNoticeSent(sent, e.i18n.T(MsgMessageQueued)) {
		t.Fatalf("sent = %v, want no MsgMessageQueued", sent)
	}
	full := strings.SplitN(e.i18n.T(MsgQueueFull), "(", 2)[0]
	if !queueNoticeSent(sent, full) {
		t.Fatalf("sent = %v, want MsgQueueFull", sent)
	}
}

// The default keeps the notice.
func TestQueueNoticeDefault_RepliesQueued(t *testing.T) {
	e, p, _, _ := setupQueueNoticeSession(t, newQueuingSession("default"))
	e.ReceiveMessage(p, queueNoticeMessage("follow-up", 1_000))
	if !queueNoticeSent(p.getSent(), e.i18n.T(MsgMessageQueued)) {
		t.Fatalf("sent = %v, want MsgMessageQueued", p.getSent())
	}
}
