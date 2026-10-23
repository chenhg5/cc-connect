package core

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

type busySteeringSession struct {
	*queuingAgentSession
	mu       sync.Mutex
	err      error
	inactive bool
	steered  []string
}

func (s *busySteeringSession) SteerUserInput(prompt string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steered = append(s.steered, prompt)
	return !s.inactive && s.err == nil, s.err
}

func TestBusySteering_RoutingAndDeliveryOutcome(t *testing.T) {
	for _, tt := range []struct {
		name, mode                               string
		err                                      error
		inactive, unsupported, attachment, image bool
		wantQueue                                int
		wantHistory                              int
		wantSteer                                int
		ack                                      MsgKey
	}{
		{name: "accepted", mode: "steer", wantHistory: 1, wantSteer: 1, ack: MsgMessageSteered},
		{name: "default queue", wantQueue: 1, ack: MsgMessageQueued},
		{name: "reload disables steering", mode: "queue", wantQueue: 1, ack: MsgMessageQueued},
		{name: "unsupported", mode: "steer", unsupported: true, wantQueue: 1, ack: MsgMessageQueued},
		{name: "file", mode: "steer", attachment: true, wantQueue: 1, ack: MsgMessageQueued},
		{name: "image", mode: "steer", image: true, wantQueue: 1, ack: MsgMessageQueued},
		{name: "turn ended", mode: "steer", inactive: true, wantSteer: 1, wantQueue: 1, ack: MsgMessageQueued},
		{name: "explicit rejection", mode: "steer", err: ErrSteerRejected, wantSteer: 1, wantQueue: 1, ack: MsgMessageQueued},
		{name: "timeout is not retried", mode: "steer", err: errors.New("ack timed out"), wantHistory: 2, wantSteer: 1, ack: MsgMessageSteerUnconfirmed},
		{name: "transport failure is not retried", mode: "steer", err: errors.New("connection closed"), wantHistory: 2, wantSteer: 1, ack: MsgMessageSteerUnconfirmed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := &stubPlatformEngine{n: "test"}
			s := &busySteeringSession{queuingAgentSession: newQueuingSession("busy"), err: tt.err, inactive: tt.inactive}
			var target AgentSession = s
			if tt.unsupported {
				target = s.queuingAgentSession
			}
			e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
			t.Cleanup(func() { _ = e.Stop() })
			e.SetBusyMessageMode("steer")
			e.SetBusyMessageMode(tt.mode)
			const key = "test:busy"
			state := &interactiveState{agentSession: target, platform: p}
			e.interactiveStates[key] = state
			session := e.sessions.GetOrCreateActive(key)
			gen, ok := session.TryLock()
			if !ok {
				t.Fatal("lock")
			}
			defer session.UnlockWithoutUpdate(gen)
			accepted := 0
			msg := &Message{SessionKey: key, Platform: p.Name(), MessageID: "follow-up", Content: "add unit tests", ReplyCtx: "ctx", UserMessageTimeMs: 2000}
			msg.OnAccepted = func() { accepted++ }
			if tt.attachment {
				msg.Files = []FileAttachment{{FileName: "notes.txt", Data: []byte("notes")}}
			}
			if tt.image {
				msg.Images = []ImageAttachment{{MimeType: "image/png", Data: []byte("image")}}
			}
			e.ReceiveMessage(p, msg)
			state.mu.Lock()
			depth := len(state.pendingMessages)
			state.mu.Unlock()
			if depth != tt.wantQueue {
				t.Fatalf("queue=%d, want %d", depth, tt.wantQueue)
			}
			if got := len(session.GetHistory(0)); got != tt.wantHistory {
				t.Fatalf("history=%d, want %d", got, tt.wantHistory)
			}
			if len(s.steered) != tt.wantSteer {
				t.Fatalf("steering calls=%v", s.steered)
			}
			if accepted != 1 {
				t.Fatalf("acceptance callbacks=%d", accepted)
			}
			if !strings.Contains(strings.Join(p.getSent(), "\n"), e.i18n.T(tt.ack)) {
				t.Fatalf("replies=%v", p.getSent())
			}
		})
	}
}

func TestCmdPs_NativeSteeringNeverStartsSecondTurn(t *testing.T) {
	for _, command := range []string{"/ps", "/btw"} {
		for _, tt := range []struct {
			name     string
			inactive bool
			err      error
			ack      MsgKey
		}{
			{name: "accepted", ack: MsgPsSent},
			{name: "ended", inactive: true, ack: MsgPsNoSession},
			{name: "rejected", err: ErrSteerRejected, ack: MsgPsSendFailed},
			{name: "unconfirmed", err: errors.New("timeout"), ack: MsgMessageSteerUnconfirmed},
		} {
			t.Run(command+"/"+tt.name, func(t *testing.T) {
				p := &stubPlatformEngine{n: "test"}
				s := &busySteeringSession{queuingAgentSession: newQueuingSession("ps"), err: tt.err, inactive: tt.inactive}
				e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
				t.Cleanup(func() { _ = e.Stop() })
				const key = "test:ps"
				e.interactiveStates[key] = &interactiveState{agentSession: s, platform: p}
				session := e.sessions.GetOrCreateActive(key)
				gen, _ := session.TryLock()
				defer session.UnlockWithoutUpdate(gen)
				e.ReceiveMessage(p, &Message{SessionKey: key, Platform: p.Name(), Content: command + " add tests", ReplyCtx: "ctx"})
				if len(s.sendCalls) != 0 {
					t.Fatalf("supplement incorrectly called Send: %v", s.sendCalls)
				}
				if len(s.steered) != 1 || s.steered[0] != "add tests" {
					t.Fatalf("steered=%v", s.steered)
				}
				if !strings.Contains(strings.Join(p.getSent(), "\n"), e.i18n.T(tt.ack)) {
					t.Fatalf("replies=%v", p.getSent())
				}
			})
		}
	}
}

func TestCmdPs_PreservesSendBasedSupplementAdapters(t *testing.T) {
	for _, command := range []string{"/ps", "/btw"} {
		t.Run(command, func(t *testing.T) {
			p := &stubPlatformEngine{n: "test"}
			s := newQueuingSession("legacy")
			e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
			t.Cleanup(func() { _ = e.Stop() })
			const key = "test:legacy"
			e.interactiveStates[key] = &interactiveState{agentSession: s, platform: p}
			session := e.sessions.GetOrCreateActive(key)
			gen, _ := session.TryLock()
			defer session.UnlockWithoutUpdate(gen)
			e.ReceiveMessage(p, &Message{SessionKey: key, Content: command + " preserve native adapter behavior", ReplyCtx: "ctx"})
			if len(s.sendCalls) != 1 || s.sendCalls[0] != "preserve native adapter behavior" {
				t.Fatalf("Send calls=%v", s.sendCalls)
			}
			if !strings.Contains(strings.Join(p.getSent(), "\n"), e.i18n.T(MsgPsSent)) {
				t.Fatalf("replies=%v", p.getSent())
			}
		})
	}
}
