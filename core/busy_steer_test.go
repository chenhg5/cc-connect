package core

import (
	"context"
	"sync"
	"testing"
	"time"
)

// busySteerAgentSession wraps resultAgentSession and implements BusySteerSender.
// supportsBusySteer controls whether the engine should attempt steering.
type busySteerAgentSession struct {
	*resultAgentSession
	supportsBusySteer bool
	sendMu            sync.Mutex
	sendCount         int
	sendErr           error
}

func newBusySteerAgentSession(result string, supports bool) *busySteerAgentSession {
	return &busySteerAgentSession{
		resultAgentSession: newResultAgentSession(result),
		supportsBusySteer:  supports,
	}
}

func (s *busySteerAgentSession) SupportsBusySteer() bool { return s.supportsBusySteer }

func (s *busySteerAgentSession) Send(prompt string, id string, imgs []ImageAttachment, files []FileAttachment) error {
	s.sendMu.Lock()
	s.sendCount++
	err := s.sendErr
	s.sendMu.Unlock()
	if err != nil {
		return err
	}
	return s.resultAgentSession.Send(prompt, id, imgs, files)
}

// setupBusySession creates an engine with a busy session locked and an
// interactiveState pointing at the given agent session. Returns the engine,
// platform, key, and the session (still locked) so the caller can Unlock.
func setupBusySession(t *testing.T, agentSession AgentSession) (*Engine, *stubPlatformEngine, string, *Session) {
	t.Helper()
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	key := "test:busy-steer:user1"
	session := e.sessions.GetOrCreateActive(key)
	if gen, locked := session.TryLock(); !locked {
		t.Fatal("expected to lock session for busy setup")
	} else {
		_ = gen
	}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = &interactiveState{
		agentSession: agentSession,
		busySession:  session,
		platform:     p,
		replyCtx:     "busy-reply-ctx",
	}
	e.interactiveMu.Unlock()
	t.Cleanup(func() { _ = e.Stop() })
	return e, p, key, session
}

type ackSteerAgentSession struct {
	*busySteerAgentSession
	disposition SteerDisposition
	requestID   string
	sendErr     error
	steerCalls  int
	steerMu     sync.Mutex
	lastText    string
}

func newAckSteerAgentSession(disp SteerDisposition) *ackSteerAgentSession {
	return &ackSteerAgentSession{
		busySteerAgentSession: newBusySteerAgentSession("ack result", true),
		disposition:           disp,
		requestID:             "cc-connect-7",
	}
}

func (s *ackSteerAgentSession) SendBusySteer(_ context.Context, text, messageID string, _ []ImageAttachment, _ []FileAttachment) (SteerResult, error) {
	s.steerMu.Lock()
	s.steerCalls++
	s.lastText = text
	s.steerMu.Unlock()
	if s.sendErr != nil {
		return SteerResult{}, s.sendErr
	}
	return SteerResult{Disposition: s.disposition, RequestID: s.requestID}, nil
}

func waitBusyCondition(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for !predicate() {
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("busy-input condition did not become true")
		}
	}
}

func TestBusySteer_AckDispositionReplies(t *testing.T) {
	for _, tc := range []struct {
		name        string
		disposition SteerDisposition
		notice      MsgKey
	}{
		{"queued", SteerQueued, MsgSteerQueued},
		{"started", SteerStarted, MsgSteerStarted},
		{"handled", SteerHandled, MsgSteerHandled},
		{"unknown", SteerDisposition("future-value"), MsgSteerDeliveryUnknown},
		{"missing", "", MsgSteerDeliveryUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			as := newAckSteerAgentSession(tc.disposition)
			e, p, key, session := setupBusySession(t, as)
			e.SetBusyMessageMode("steer")
			defer session.Unlock(0)
			e.ReceiveMessage(p, &Message{SessionKey: key, Content: "correction", MessageID: "correction"})
			waitBusyCondition(t, func() bool {
				for _, text := range p.getSent() {
					if text == e.i18n.T(tc.notice) {
						return true
					}
				}
				return false
			})
			sent := p.getSent()
			if len(sent) != 2 || sent[0] != e.i18n.T(MsgSteerWaiting) {
				t.Fatalf("receipts=%v", sent)
			}
			as.steerMu.Lock()
			calls := as.steerCalls
			as.steerMu.Unlock()
			if calls != 1 {
				t.Fatalf("native sends=%d", calls)
			}
		})
	}
}

func TestBusySteer_UnsupportedAndDefaultUseOrdinaryQueue(t *testing.T) {
	for _, tc := range []struct {
		name            string
		enabled, native bool
	}{
		{"unsupported", true, false}, {"default", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			as := newAckSteerAgentSession(SteerQueued)
			as.supportsBusySteer = tc.native
			e, p, key, session := setupBusySession(t, as)
			defer session.Unlock(0)
			if tc.enabled {
				e.SetBusyMessageMode("steer")
			}
			e.ReceiveMessage(p, &Message{SessionKey: key, Content: "queued correction"})
			if queuedCountForTest(e, key) != 1 {
				t.Fatal("ordinary fallback was lost")
			}
			as.steerMu.Lock()
			calls := as.steerCalls
			as.steerMu.Unlock()
			if calls != 0 {
				t.Fatal("unsupported/default mode attempted native send")
			}
		})
	}
}

func queuedCountForTest(e *Engine, key string) int {
	e.interactiveMu.Lock()
	defer e.interactiveMu.Unlock()
	state := e.interactiveStates[key]
	if state == nil {
		return 0
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return len(state.pendingMessages)
}
