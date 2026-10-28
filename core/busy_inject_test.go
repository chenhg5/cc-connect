package core

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// midTurnAgentSession records sends like queuingAgentSession and advertises
// MidTurnInputSender, like an agent whose CLI reads input from one live stdin.
type midTurnAgentSession struct {
	*queuingAgentSession
	sendErr  error
	midTurns int
}

func newMidTurnSession(id string) *midTurnAgentSession {
	return &midTurnAgentSession{queuingAgentSession: newQueuingSession(id)}
}

func (s *midTurnAgentSession) SendMidTurn(prompt string, msgID string, images []ImageAttachment, files []FileAttachment) error {
	if s.sendErr != nil {
		return s.sendErr
	}
	s.sendMu.Lock()
	s.midTurns++
	s.sendMu.Unlock()
	return s.Send(prompt, msgID, images, files)
}

// busySteeringSession advertises UserInputSteerer, like an agent with a native steer RPC.
type busySteeringSession struct {
	*queuingAgentSession
	mu       sync.Mutex
	steered  []string
	steerOK  bool
	steerErr error
}

func (s *busySteeringSession) SteerUserInput(prompt string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.steerErr != nil || !s.steerOK {
		return false, s.steerErr
	}
	s.steered = append(s.steered, prompt)
	return true, nil
}

// midTurnResultSession completes every turn and also accepts mid-turn input,
// so the idle path can be checked against a capable agent.
type midTurnResultSession struct {
	*resultAgentSession
}

func (s *midTurnResultSession) SendMidTurn(prompt string, msgID string, images []ImageAttachment, files []FileAttachment) error {
	return s.Send(prompt, msgID, images, files)
}

// setupBusySession installs agentSession as the live agent for key and locks
// the session as if a turn were in flight.
func setupBusySession(t *testing.T, agentSession AgentSession) (*Engine, *stubPlatformEngine, *interactiveState, *Session) {
	t.Helper()
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetBusyAction(BusyActionInject)
	key := "test:user1"
	state := &interactiveState{agentSession: agentSession, platform: p, replyCtx: "turn-ctx"}
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

func busyMessage(content string) *Message {
	return &Message{
		SessionKey: "test:user1",
		Platform:   "test",
		MessageID:  "msg-busy",
		Content:    content,
		ReplyCtx:   "busy-ctx",
	}
}

func pendingCount(state *interactiveState) int {
	state.mu.Lock()
	defer state.mu.Unlock()
	return len(state.pendingMessages)
}

func sentContains(sent []string, want string) bool {
	for _, s := range sent {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}

func TestBusyMessage_MidTurnSender_InjectsInsteadOfQueueing(t *testing.T) {
	sess := newMidTurnSession("busy-inject")
	e, p, state, session := setupBusySession(t, sess)
	accepted := false
	msg := busyMessage("also cover the error path")
	msg.OnAccepted = func() { accepted = true }

	e.ReceiveMessage(p, msg)

	sess.sendMu.Lock()
	calls := append([]string(nil), sess.sendCalls...)
	sess.sendMu.Unlock()
	if len(calls) != 1 || !strings.Contains(calls[0], "also cover the error path") {
		t.Fatalf("Send calls = %v, want one call carrying the message", calls)
	}
	if sess.midTurns != 1 {
		t.Fatalf("SendMidTurn calls = %d, want 1 (busy input must not go through plain Send)", sess.midTurns)
	}
	if n := pendingCount(state); n != 0 {
		t.Fatalf("pending messages = %d, want 0 (message must not be queued)", n)
	}
	sent := p.getSent()
	if !sentContains(sent, e.i18n.T(MsgMessageInjected)) {
		t.Fatalf("sent = %v, want MsgMessageInjected", sent)
	}
	if sentContains(sent, e.i18n.T(MsgMessageQueued)) {
		t.Fatalf("sent = %v, unexpected MsgMessageQueued", sent)
	}
	if !accepted {
		t.Fatal("OnAccepted was not called for the injected message")
	}
	hist := session.GetHistory(0)
	if len(hist) != 1 || hist[0].Role != "user" || hist[0].Content != "also cover the error path" {
		t.Fatalf("history = %#v, want the injected user message", hist)
	}
}

func TestBusyMessage_ConsecutiveMessagesAllInject(t *testing.T) {
	sess := newMidTurnSession("busy-inject-many")
	e, p, state, _ := setupBusySession(t, sess)

	for _, text := range []string{"first", "second", "third"} {
		e.ReceiveMessage(p, busyMessage(text))
	}

	sess.sendMu.Lock()
	n := len(sess.sendCalls)
	sess.sendMu.Unlock()
	if n != 3 {
		t.Fatalf("Send calls = %d, want 3", n)
	}
	if q := pendingCount(state); q != 0 {
		t.Fatalf("pending messages = %d, want 0", q)
	}
}

func TestBusyMessage_UserInputSteerer_SteersTextIntoTurn(t *testing.T) {
	sess := &busySteeringSession{queuingAgentSession: newQueuingSession("busy-steer"), steerOK: true}
	e, p, state, _ := setupBusySession(t, sess)

	e.ReceiveMessage(p, busyMessage("use the staging config"))

	sess.mu.Lock()
	steered := append([]string(nil), sess.steered...)
	sess.mu.Unlock()
	if len(steered) != 1 || !strings.Contains(steered[0], "use the staging config") {
		t.Fatalf("steered = %v, want the message", steered)
	}
	sess.sendMu.Lock()
	sends := len(sess.sendCalls)
	sess.sendMu.Unlock()
	if sends != 0 {
		t.Fatalf("Send calls = %d, want 0 (steer must be used instead of Send)", sends)
	}
	if n := pendingCount(state); n != 0 {
		t.Fatalf("pending messages = %d, want 0", n)
	}
	if !sentContains(p.getSent(), e.i18n.T(MsgMessageInjected)) {
		t.Fatalf("sent = %v, want MsgMessageInjected", p.getSent())
	}
}

func TestBusyMessage_FallsBackToQueue(t *testing.T) {
	tests := []struct {
		name    string
		session func() AgentSession
		msg     func() *Message
		prep    func(*interactiveState)
	}{
		{
			name:    "agent without mid-turn support",
			session: func() AgentSession { return newQueuingSession("plain") },
		},
		{
			name: "send error",
			session: func() AgentSession {
				s := newMidTurnSession("send-err")
				s.sendErr = errors.New("broken pipe")
				return s
			},
		},
		{
			name: "steerer reports no active turn",
			session: func() AgentSession {
				return &busySteeringSession{queuingAgentSession: newQueuingSession("no-turn")}
			},
		},
		{
			name: "steerer error",
			session: func() AgentSession {
				return &busySteeringSession{queuingAgentSession: newQueuingSession("steer-err"), steerErr: errors.New("rpc closed")}
			},
		},
		{
			name: "steerer with attachment",
			session: func() AgentSession {
				return &busySteeringSession{queuingAgentSession: newQueuingSession("steer-img"), steerOK: true}
			},
			msg: func() *Message {
				m := busyMessage("see screenshot")
				m.Images = []ImageAttachment{{MimeType: "image/png", Data: []byte{1}}}
				return m
			},
		},
		{
			// Earlier messages are already waiting; injecting this one would
			// let it overtake them.
			name:    "queue already non-empty",
			session: func() AgentSession { return newMidTurnSession("ordered") },
			prep: func(st *interactiveState) {
				st.pendingMessages = append(st.pendingMessages, queuedMessage{messageID: "earlier", content: "earlier"})
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, p, state, _ := setupBusySession(t, tt.session())
			if tt.prep != nil {
				tt.prep(state)
			}
			before := pendingCount(state)
			msg := busyMessage("follow-up")
			if tt.msg != nil {
				msg = tt.msg()
			}

			e.ReceiveMessage(p, msg)

			if got := pendingCount(state); got != before+1 {
				t.Fatalf("pending messages = %d, want %d", got, before+1)
			}
			sent := p.getSent()
			if !sentContains(sent, e.i18n.T(MsgMessageQueued)) {
				t.Fatalf("sent = %v, want MsgMessageQueued", sent)
			}
			if sentContains(sent, e.i18n.T(MsgMessageInjected)) {
				t.Fatalf("sent = %v, unexpected MsgMessageInjected", sent)
			}
		})
	}
}

func TestBusyMessage_AgentStillStarting_Queues(t *testing.T) {
	// The first message's agent process is still starting (placeholder state
	// with no agentSession yet): there is nothing to inject into, so the
	// message waits for the startup turn as before (issue #565).
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetBusyAction(BusyActionInject)
	key := "test:user1"
	state := &interactiveState{platform: p}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()
	session := e.sessions.GetOrCreateActive(key)
	if _, ok := session.TryLock(); !ok {
		t.Fatal("expected to lock session")
	}
	defer session.Unlock(0)

	e.ReceiveMessage(p, busyMessage("while starting"))

	if n := pendingCount(state); n != 1 {
		t.Fatalf("pending messages = %d, want 1", n)
	}
}

func TestIdleMessage_MidTurnSender_StartsNormalTurn(t *testing.T) {
	agentSession := &midTurnResultSession{resultAgentSession: newResultAgentSession("idle turn done")}
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &resultAgent{session: agentSession}, []Platform{p}, "", LangEnglish)
	e.SetBusyAction(BusyActionInject)

	e.ReceiveMessage(p, &Message{
		SessionKey: "test:user1",
		Platform:   "test",
		MessageID:  "msg-idle",
		Content:    "start the task",
		ReplyCtx:   "idle-ctx",
	})

	sent := waitForPlatformSend(p, 1, 3*time.Second)
	if len(sent) == 0 || sent[0] != "idle turn done" {
		t.Fatalf("sent = %v, want the turn result", sent)
	}
	if sentContains(sent, e.i18n.T(MsgMessageInjected)) {
		t.Fatalf("sent = %v, idle message must not be injected", sent)
	}
	if len(agentSession.sentPrompts) != 1 || !strings.Contains(agentSession.sentPrompts[0], "start the task") {
		t.Fatalf("prompts = %#v, want one normal turn", agentSession.sentPrompts)
	}
}

// busy_action defaults to "queue": even an agent that can take mid-turn input
// gets the message after the running turn, as before.
func TestBusyMessage_DefaultBusyActionQueues(t *testing.T) {
	sess := newMidTurnSession("default-queue")
	e, p, state, _ := setupBusySession(t, sess)
	e.SetBusyAction("")

	e.ReceiveMessage(p, busyMessage("follow-up"))

	if sess.midTurns != 0 {
		t.Fatalf("SendMidTurn calls = %d, want 0 with the default busy_action", sess.midTurns)
	}
	if n := pendingCount(state); n != 1 {
		t.Fatalf("pending messages = %d, want 1", n)
	}
	if !sentContains(p.getSent(), e.i18n.T(MsgMessageQueued)) {
		t.Fatalf("sent = %v, want MsgMessageQueued", p.getSent())
	}
}

func TestSetBusyAction_Normalizes(t *testing.T) {
	e := NewEngine("test", &stubAgent{}, nil, "", LangEnglish)
	if e.busyAction != BusyActionQueue {
		t.Fatalf("default busyAction = %q, want %q", e.busyAction, BusyActionQueue)
	}
	for in, want := range map[string]string{
		"inject":   BusyActionInject,
		" INJECT ": BusyActionInject,
		"queue":    BusyActionQueue,
		"":         BusyActionQueue,
		"drop":     BusyActionQueue,
	} {
		e.SetBusyAction(in)
		if e.busyAction != want {
			t.Fatalf("SetBusyAction(%q) = %q, want %q", in, e.busyAction, want)
		}
	}
}
