package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// steerableAgentSession is a queuingAgentSession that also implements
// MidTurnSteerer, so tests can cover messages folded into a running turn.
type steerableAgentSession struct {
	*queuingAgentSession
	supports bool
	err      error
	images   []ImageAttachment
	files    []FileAttachment
}

func newSteerableSession(id string) *steerableAgentSession {
	return &steerableAgentSession{supports: true, queuingAgentSession: newQueuingSession(id)}
}

func (s *steerableAgentSession) SupportsMidTurnSteering() bool { return s.supports }

func (s *steerableAgentSession) Send(prompt, messageID string, images []ImageAttachment, files []FileAttachment) error {
	if s.err != nil {
		return s.err
	}
	s.images = images
	s.files = files
	return s.queuingAgentSession.Send(prompt, messageID, images, files)
}

func TestTrySteerBusyMessage_HappyPathWithAttachments(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	as := newSteerableSession("steer-ok")
	agent := &controllableAgent{startSessionFn: func(context.Context, string) (AgentSession, error) { return as, nil }}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.SetSteerWhileBusy(true)

	key := "test:user1"
	msg := func(content, ctx string) *Message {
		return &Message{SessionKey: key, Platform: "test", UserID: "u1", UserName: "user", Content: content, ReplyCtx: ctx}
	}
	e.handleMessage(p, msg("start a long task", "ctx-1"))
	waitForQueuedSends(t, as.queuingAgentSession, 1)

	supplement := msg("do not change the nav bar", "ctx-2")
	supplement.Images = []ImageAttachment{{MimeType: "image/png", Data: []byte("img"), FileName: "shot.png"}}
	supplement.Files = []FileAttachment{{MimeType: "text/plain", Data: []byte("doc"), FileName: "notes.txt"}}
	e.handleMessage(p, supplement)

	as.sendMu.Lock()
	calls := len(as.sendCalls)
	last := as.sendCalls[calls-1]
	as.sendMu.Unlock()
	if calls != 2 || last != "do not change the nav bar" {
		t.Fatalf("agent sends = %q, want the supplement sent immediately", as.sendCalls)
	}
	if len(as.images) != 1 || as.images[0].FileName != "shot.png" || len(as.files) != 1 {
		t.Fatalf("attachments not forwarded: images=%v files=%v", as.images, as.files)
	}

	e.interactiveMu.Lock()
	state := e.interactiveStates[key]
	e.interactiveMu.Unlock()
	state.mu.Lock()
	queued := len(state.pendingMessages)
	state.mu.Unlock()
	if queued != 0 {
		t.Fatalf("pendingMessages = %d, want 0 (steered, not queued)", queued)
	}

	session := e.sessions.GetOrCreateActive(key)
	found := false
	for _, h := range session.GetHistory(0) {
		if h.Role == "user" && h.Content == "do not change the nav bar" {
			found = true
		}
	}
	if !found {
		t.Fatal("steered message was not recorded in session history")
	}
	if got := p.getSent(); len(got) != 0 {
		t.Fatalf("steering must not send an extra acknowledgement; replies=%q", got)
	}
}

func TestTrySteerBusyMessage_QueueNotEmptyFallsBack(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetSteerWhileBusy(true)
	as := newSteerableSession("steer-queue")
	key := "test:user1"
	session := e.sessions.GetOrCreateActive(key)
	e.interactiveStates[key] = &interactiveState{
		agentSession: as, platform: p,
		pendingMessages: []queuedMessage{{content: "earlier"}},
	}
	if e.trySteerBusyMessage(p, &Message{SessionKey: key, Content: "later"}, session, e.sessions, key) {
		t.Fatal("must not steer while the queue is non-empty")
	}
	as.sendMu.Lock()
	defer as.sendMu.Unlock()
	if len(as.sendCalls) != 0 {
		t.Fatalf("Send called %d times, want 0", len(as.sendCalls))
	}
}

func TestTrySteerBusyMessage_PendingInteractionFallsBack(t *testing.T) {
	cases := map[string]func(*interactiveState){
		"permission": func(s *interactiveState) { s.pending = &pendingPermission{} },
		"question":   func(s *interactiveState) { s.pendingUserInput = &pendingUserInput{} },
	}
	for name, decorate := range cases {
		t.Run(name, func(t *testing.T) {
			p := &stubPlatformEngine{n: "test"}
			e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
			e.SetSteerWhileBusy(true)
			as := newSteerableSession("steer-pending-" + name)
			key := "test:user1"
			session := e.sessions.GetOrCreateActive(key)
			state := &interactiveState{agentSession: as, platform: p}
			decorate(state)
			e.interactiveStates[key] = state
			if e.trySteerBusyMessage(p, &Message{SessionKey: key, Content: "text"}, session, e.sessions, key) {
				t.Fatal("must not steer while an interaction is pending")
			}
		})
	}
}

func TestTrySteerBusyMessage_StaleRedeliveryDropped(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetSteerWhileBusy(true)
	as := newSteerableSession("steer-stale")
	key := "test:user1"
	session := e.sessions.GetOrCreateActive(key)
	e.interactiveStates[key] = &interactiveState{
		agentSession: as, platform: p, lastSteeredUserMessageTimeMs: 2000,
	}
	if e.trySteerBusyMessage(p, &Message{SessionKey: key, Content: "old", UserMessageTimeMs: 1000}, session, e.sessions, key) {
		t.Fatal("stale redelivery must not be steered twice")
	}
}

func TestTrySteerBusyMessage_SendErrorFallsBackToQueue(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	as := newSteerableSession("steer-err")
	agent := &controllableAgent{startSessionFn: func(context.Context, string) (AgentSession, error) { return as, nil }}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.SetSteerWhileBusy(true)
	key := "test:user1"
	e.handleMessage(p, &Message{SessionKey: key, Platform: "test", UserID: "u1", Content: "start", ReplyCtx: "ctx-1"})
	waitForQueuedSends(t, as.queuingAgentSession, 1)

	as.err = errors.New("conn closed")
	e.handleMessage(p, &Message{SessionKey: key, Platform: "test", UserID: "u1", Content: "supplement", ReplyCtx: "ctx-2"})

	e.interactiveMu.Lock()
	state := e.interactiveStates[key]
	e.interactiveMu.Unlock()
	state.mu.Lock()
	queued := len(state.pendingMessages)
	state.mu.Unlock()
	if queued != 1 {
		t.Fatalf("pendingMessages = %d, want 1 (fallback queue)", queued)
	}
	queuedReply := false
	for _, line := range p.getSent() {
		if strings.Contains(line, "will process after the current task finishes") {
			queuedReply = true
		}
	}
	if !queuedReply {
		t.Fatalf("queued receipt missing; replies=%q", p.getSent())
	}
}

func TestCmdPs_WritesHistoryAndUsesSteerPath(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	as := newSteerableSession("ps-history")
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	key := "test:user1"
	session := e.sessions.GetOrCreateActive(key)
	session.TryLock() // busy, so /ps accepts the supplement
	e.interactiveStates[key] = &interactiveState{agentSession: as, platform: p, busySession: session}

	e.cmdPs(p, &Message{SessionKey: key, Platform: "test", ReplyCtx: "ctx"}, []string{"nav 不改"})

	as.sendMu.Lock()
	calls := append([]string(nil), as.sendCalls...)
	as.sendMu.Unlock()
	if len(calls) != 1 || calls[0] != "nav 不改" {
		t.Fatalf("agent sends = %q, want one supplement", calls)
	}
	found := false
	for _, h := range session.GetHistory(0) {
		if h.Role == "user" && h.Content == "nav 不改" {
			found = true
		}
	}
	if !found {
		t.Fatal("/ps must record the supplement in session history")
	}
	sent := p.getSent()
	if len(sent) != 1 || !strings.Contains(sent[0], "P.S. delivered") {
		t.Fatalf("replies = %q, want one P.S. delivered ack", sent)
	}
}

// The feature is opt-in: with the flag off the message must follow the queue
// path and keep the queued receipt.
func TestSteerWhileBusy_DisabledQueuesInstead(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	as := newSteerableSession("steer-off")
	agent := &controllableAgent{startSessionFn: func(context.Context, string) (AgentSession, error) { return as, nil }}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	key := "test:user1"
	e.handleMessage(p, &Message{SessionKey: key, Platform: "test", UserID: "u1", Content: "start", ReplyCtx: "ctx-1"})
	waitForQueuedSends(t, as.queuingAgentSession, 1)
	e.handleMessage(p, &Message{SessionKey: key, Platform: "test", UserID: "u1", Content: "supplement", ReplyCtx: "ctx-2"})

	e.interactiveMu.Lock()
	state := e.interactiveStates[key]
	e.interactiveMu.Unlock()
	state.mu.Lock()
	queued := len(state.pendingMessages)
	state.mu.Unlock()
	if queued != 1 {
		t.Fatalf("pendingMessages = %d, want 1 with steering disabled", queued)
	}
	as.sendMu.Lock()
	calls := len(as.sendCalls)
	as.sendMu.Unlock()
	if calls != 1 {
		t.Fatalf("agent sends = %d, want only the original turn", calls)
	}
}

// Agents that do not implement MidTurnSteerer must keep queueing.
func TestSteerWhileBusy_UnsupportedAgentQueues(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	as := newSteerableSession("steer-unsupported")
	as.supports = false
	agent := &controllableAgent{startSessionFn: func(context.Context, string) (AgentSession, error) { return as, nil }}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.SetSteerWhileBusy(true)

	key := "test:user1"
	e.handleMessage(p, &Message{SessionKey: key, Platform: "test", UserID: "u1", Content: "start", ReplyCtx: "ctx-1"})
	waitForQueuedSends(t, as.queuingAgentSession, 1)
	e.handleMessage(p, &Message{SessionKey: key, Platform: "test", UserID: "u1", Content: "supplement", ReplyCtx: "ctx-2"})

	e.interactiveMu.Lock()
	state := e.interactiveStates[key]
	e.interactiveMu.Unlock()
	state.mu.Lock()
	queued := len(state.pendingMessages)
	state.mu.Unlock()
	if queued != 1 {
		t.Fatalf("pendingMessages = %d, want 1 for an unsupported agent", queued)
	}
}

// A steered supplement must not disturb the in-flight turn's own result: the
// turn still ends with its EventResult and the reply reaches the platform.
func TestSteerWhileBusy_TurnResultStillDelivered(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	as := newSteerableSession("steer-result")
	agent := &controllableAgent{startSessionFn: func(context.Context, string) (AgentSession, error) { return as, nil }}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.SetSteerWhileBusy(true)

	key := "test:user1"
	e.handleMessage(p, &Message{SessionKey: key, Platform: "test", UserID: "u1", Content: "start", ReplyCtx: "ctx-1"})
	waitForQueuedSends(t, as.queuingAgentSession, 1)
	e.handleMessage(p, &Message{SessionKey: key, Platform: "test", UserID: "u1", Content: "supplement", ReplyCtx: "ctx-2"})
	waitForQueuedSends(t, as.queuingAgentSession, 2)

	as.events <- Event{Type: EventResult, Content: "did both", Done: true}
	waitForSessionIdle(t, e.sessions.GetOrCreateActive(key))

	found := false
	for _, line := range p.getSent() {
		if strings.Contains(line, "did both") {
			found = true
		}
	}
	if !found {
		t.Fatalf("turn result missing after steering; replies=%q", p.getSent())
	}
}

// Events that arrive after the foreground turn ended belong to the unsolicited
// reader; its text must be relayed, never dropped.
func TestSteerWhileBusy_LateTurnTextRelayed(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	as := newSteerableSession("steer-late")
	agent := &controllableAgent{startSessionFn: func(context.Context, string) (AgentSession, error) { return as, nil }}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.SetSteerWhileBusy(true)

	key := "test:user1"
	e.handleMessage(p, &Message{SessionKey: key, Platform: "test", UserID: "u1", Content: "start", ReplyCtx: "ctx-1"})
	waitForQueuedSends(t, as.queuingAgentSession, 1)
	as.events <- Event{Type: EventResult, Content: "first done", Done: true}
	waitForSessionIdle(t, e.sessions.GetOrCreateActive(key))

	as.events <- Event{Type: EventText, Content: "late supplemental answer"}
	as.events <- Event{Type: EventResult, Content: "late supplemental answer", Done: true}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, line := range p.getSent() {
			if strings.Contains(line, "late supplemental answer") {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("late turn text was dropped; replies=%q", p.getSent())
}
