package core

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Separate Alive from Close so the real Engine can see unexpected exit
// while a native ACK and later ordinary messages are still pending.
type exitingSteerSession struct {
	*busySteerCUJSession
	dead atomic.Bool
}

func (s *exitingSteerSession) Alive() bool { return !s.dead.Load() }

func TestBusySteer_UnexpectedExitClassifiesQueue(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result SteerResult
		ackErr error
		want   []string
		paused bool
		eof    bool
	}{
		{"definitely_rejected", SteerResult{}, &SteerNotAcceptedError{Err: errors.New("not sent")}, []string{"supplement", "later-one", "later-two"}, false, false},
		{"definitely_rejected_eof", SteerResult{}, &SteerNotAcceptedError{Err: errors.New("not sent")}, []string{"supplement", "later-one", "later-two"}, false, true},
		{"started_ack_no_replay", SteerResult{Disposition: SteerStarted}, nil, []string{"later-one", "later-two"}, false, false},
		{"queued_ack_no_replay", SteerResult{Disposition: SteerQueued}, nil, []string{"later-one", "later-two"}, false, false},
		{"handled_ack_no_replay", SteerResult{Disposition: SteerHandled}, nil, []string{"later-one", "later-two"}, false, false},
		{"unknown_quarantines_queue", SteerResult{}, errors.New("EOF without ACK"), nil, true, false},
		{"unknown_eof_quarantines_queue", SteerResult{}, errors.New("EOF without ACK"), nil, true, true},
		{"started_ack_eof_no_replay", SteerResult{Disposition: SteerStarted}, nil, []string{"later-one", "later-two"}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := &exitingSteerSession{busySteerCUJSession: &busySteerCUJSession{
				events: make(chan Event, 16), inputs: make(chan busySteerCUJInput, 16), steers: make(chan string, 16),
				steerInputs: make(chan busySteerCUJInput, 16), ack: make(chan SteerResult, 1), ackErr: make(chan error, 1),
			}}
			fresh := newQueuingSession("fresh")
			var starts atomic.Int32
			agent := &controllableAgent{startSessionFn: func(context.Context, string) (AgentSession, error) {
				if starts.Add(1) == 1 {
					return old, nil
				}
				return fresh, nil
			}}
			p := &busySteerCUJPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
			e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
			e.SetBusyMessageMode("steer")
			t.Cleanup(func() { _ = e.Stop() })
			key := "test:steer-exit"
			send := func(text string) {
				e.ReceiveMessage(p, &Message{SessionKey: key, Platform: "test", MessageID: text, Content: text, ReplyCtx: text})
			}
			send("primary")
			select {
			case <-old.inputs:
			case <-time.After(3 * time.Second):
				t.Fatal("primary not sent")
			}
			waitBusyCondition(t, func() bool {
				e.interactiveMu.Lock()
				defer e.interactiveMu.Unlock()
				s := e.interactiveStates[key]
				if s == nil {
					return false
				}
				s.mu.Lock()
				defer s.mu.Unlock()
				return !s.steerAdmissionClosed
			})
			send("supplement")
			select {
			case <-old.steers:
			case <-time.After(3 * time.Second):
				t.Fatal("supplement not steered")
			}
			send("later-one")
			send("later-two")
			// Error starts cleanup BEFORE ACK resolves, exercising the reservation barrier.
			old.dead.Store(true)
			if tc.eof {
				close(old.events)
			} else {
				old.events <- Event{Type: EventError, Error: errors.New("process exited")}
			}
			waitBusyCondition(t, func() bool {
				e.interactiveMu.Lock()
				defer e.interactiveMu.Unlock()
				s := e.interactiveStates[key]
				if s == nil {
					return false
				}
				s.mu.Lock()
				defer s.mu.Unlock()
				return s.steerAdmissionClosed
			})
			if tc.ackErr != nil {
				old.ackErr <- tc.ackErr
			} else {
				old.ack <- tc.result
			}
			for i, want := range tc.want {
				waitBusyCondition(t, func() bool { fresh.sendMu.Lock(); defer fresh.sendMu.Unlock(); return len(fresh.sendCalls) >= i+1 })
				fresh.sendMu.Lock()
				got := fresh.sendCalls[i]
				fresh.sendMu.Unlock()
				if !strings.Contains(got, want) {
					t.Fatalf("fresh send %d = %q, want %q", i, got, want)
				}
				fresh.events <- Event{Type: EventResult, Content: "answer:" + want, Done: true}
			}
			session := e.sessions.GetOrCreateActive(key)
			waitBusyCondition(t, func() bool { return !session.Busy() })
			for _, want := range tc.want {
				n := 0
				for _, reply := range p.deliveredTo(want) {
					if strings.Contains(reply, "answer:"+want) {
						n++
					}
				}
				if n != 1 {
					t.Fatalf("reply for %q delivered %d times to its original context", want, n)
				}
			}
			fresh.sendMu.Lock()
			got := append([]string(nil), fresh.sendCalls...)
			fresh.sendMu.Unlock()
			if len(got) != len(tc.want) {
				t.Fatalf("unexpected replay: %v", got)
			}
			if tc.paused {
				send("must-not-start")
				waitBusyCondition(t, func() bool {
					for _, x := range p.getSent() {
						if x == e.i18n.T(MsgSteerInputPaused) {
							return true
						}
					}
					return false
				})
				if starts.Load() != 1 {
					t.Fatalf("unknown delivery started new process: %d", starts.Load())
				}
				send("/stop")
				send("explicitly resumed")
				waitBusyCondition(t, func() bool { fresh.sendMu.Lock(); defer fresh.sendMu.Unlock(); return len(fresh.sendCalls) == 1 })
				fresh.events <- Event{Type: EventResult, Content: "resumed answer", Done: true}
				waitBusyCondition(t, func() bool { return !session.Busy() })
			} else if starts.Load() != 2 {
				t.Fatalf("started %d sessions, want 2", starts.Load())
			}
		})
	}
}

func TestBusySteer_ExplicitPSRespectsHandoffAndUnknown(t *testing.T) {
	env := newBusySteerCUJEnv(t, "")
	env.send("primary")
	env.input("primary")
	env.awaitSteerReady()
	// Explicit native steering must also work when the automatic mode is off.
	env.e.SetBusyMessageMode("queue")
	done := make(chan struct{})
	go func() { env.send("/ps explicit"); close(done) }()
	select {
	case text := <-env.s.steers:
		if text != "explicit" {
			t.Fatalf("explicit payload = %q", text)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("explicit native input not sent")
	}
	env.send("/ps must-not-overtake")
	env.visible(env.e.i18n.T(MsgSteerFallbackQueue))
	env.s.ackErr <- errors.New("EOF before ACK")
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("explicit ACK wait did not end")
	}
	env.visible(env.e.i18n.T(MsgSteerDeliveryUnknown))
	env.visible(env.e.i18n.T(MsgSteerQueueNotDelivered))
	env.send("/ps must-not-bypass-pause")
	env.visible(env.e.i18n.T(MsgSteerInputPaused))
	env.assertNoNativeSteer()
	env.finish("primary answer")
	env.wait("idle", func() bool { return !env.e.sessions.GetOrCreateActive("test:steer-user").Busy() })
}

func TestBusySteer_CompactExitRecoversOnlyUnsentQueue(t *testing.T) {
	old := &exitingSteerSession{busySteerCUJSession: &busySteerCUJSession{events: make(chan Event, 16)}}
	fresh := newQueuingSession("new-compact")
	agent := &controllableAgent{nextSession: fresh}
	p := &busySteerCUJPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	key := "test:compact-exit"
	session := e.sessions.GetOrCreateActive(key)
	gen, ok := session.TryLock()
	if !ok {
		t.Fatal("failed to claim lock")
	}
	state := &interactiveState{agentSession: old, agent: agent, platform: p, busySession: session, steerAdmissionClosed: true, steerHandoff: true}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()
	if !e.queueMessageForBusySession(p, &Message{SessionKey: key, Platform: "test", MessageID: "queued", Content: "after compact", ReplyCtx: "queued"}, key) {
		t.Fatal("queue rejected")
	}
	done := make(chan struct{})
	go func() {
		unlocked := false
		e.processCompressEvents(state, session, e.sessions, key, p, "compact", &unlocked, false, gen)
		if !unlocked {
			session.Unlock(gen)
		}
		close(done)
	}()
	old.dead.Store(true)
	close(old.events)
	waitBusyCondition(t, func() bool { fresh.sendMu.Lock(); defer fresh.sendMu.Unlock(); return len(fresh.sendCalls) == 1 })
	fresh.sendMu.Lock()
	got := fresh.sendCalls[0]
	fresh.sendMu.Unlock()
	if !strings.Contains(got, "after compact") {
		t.Fatalf("restarted payload %q", got)
	}
	fresh.events <- Event{Type: EventResult, Content: "queued answer", Done: true}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("compression exit did not release queue")
	}
	if session.Busy() {
		t.Fatal("lock still held")
	}
	if got := p.deliveredTo("queued"); len(got) != 2 || !strings.Contains(got[1], "queued answer") {
		t.Fatalf("queued context deliveries %v", got)
	}
}
