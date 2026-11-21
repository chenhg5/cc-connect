package core

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

var errTestCapacity = errors.New("selected model is at capacity")

type capacityTestSession struct {
	stubAgentSession
	mu          sync.Mutex
	events      chan Event
	mode        string
	model       string
	models      []string
	prompts     []string
	attachments []int
	fallbacks   int
	tools       int
}

func (s *capacityTestSession) Events() <-chan Event { return s.events }

func (s *capacityTestSession) GetModel() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.model
}

func (s *capacityTestSession) Send(prompt, _ string, images []ImageAttachment, files []FileAttachment) error {
	s.mu.Lock()
	s.models = append(s.models, s.model)
	s.prompts = append(s.prompts, prompt)
	s.attachments = append(s.attachments, len(images)+len(files))
	first := len(s.models) == 1
	model := s.model
	if first && s.mode == "side-effect" {
		s.tools++
		s.events <- Event{Type: EventToolUse, ToolName: "fake-write", ToolInput: "once"}
	}
	s.mu.Unlock()
	if first && s.mode != "normal" {
		err := errTestCapacity
		switch s.mode {
		case "auth":
			err = errors.New("unauthorized")
		case "rate-limit":
			err = errors.New("HTTP 429")
		case "tool-error":
			err = errors.New("tool failed: model is at capacity")
		}
		s.events <- Event{Type: EventError, Error: err}
	} else if s.mode == "fallback-fails" {
		s.events <- Event{Type: EventError, Error: errTestCapacity}
	} else {
		s.events <- Event{Type: EventResult, Content: "answer from " + model, Done: true}
	}
	return nil
}

type capacityTestAgent struct {
	stubAgent
	session *capacityTestSession
}

func (a *capacityTestAgent) StartSession(context.Context, string) (AgentSession, error) {
	return a.session, nil
}

func TestCapacityFallback_WaitsForSendOrStop(t *testing.T) {
	for _, outcome := range []string{"success", "send-error", "stop"} {
		t.Run(outcome, func(t *testing.T) {
			p := &stubPlatformEngine{n: "test"}
			e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
			t.Cleanup(func() { _ = e.Stop() })
			as := &capacityTestSession{events: make(chan Event, 16), mode: "capacity", model: "gpt-6.1-sol"}
			state := &interactiveState{agentSession: as, platform: p, replyCtx: "ctx"}
			session := e.sessions.GetOrCreateActive("test:barrier")
			_ = as.Send("original", "m1", nil, nil)
			sendDone, finished := make(chan error, 1), make(chan struct{})
			go func() {
				e.processInteractiveEvents(state, session, e.sessions, "test:barrier", "m1", time.Now(), nil, sendDone, "ctx", 0)
				close(finished)
			}()
			env := &cujEnv{t: t, plat: p}
			env.waitFor("terminal error consumed", time.Second, func() bool { return len(as.events) == 0 })
			as.mu.Lock()
			count := len(as.models)
			as.mu.Unlock()
			if count != 1 {
				t.Fatal("continuation overlapped the original Send")
			}
			switch outcome {
			case "success":
				sendDone <- nil
			case "send-error":
				sendDone <- errors.New("turn/start rejected")
			case "stop":
				state.markStopped()
			}
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("event loop did not finish")
			}
			as.mu.Lock()
			defer as.mu.Unlock()
			want := 1
			if outcome == "success" {
				want = 2
			}
			if len(as.models) != want {
				t.Fatalf("models=%v, want %d sends", as.models, want)
			}
		})
	}
}

func (s *capacityTestSession) TryCapacityFallback(err error) (string, string, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != errTestCapacity || s.model != "gpt-6.1-sol" {
		return "", "", "", false
	}
	s.fallbacks++
	s.model = "gpt-6.1-terra"
	return "gpt-6.1-sol", "gpt-6.1-terra", "Continue the existing task without repeating completed steps.", true
}

func (s *capacityTestSession) RestoreCapacityFallback() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.model = "gpt-6.1-sol"
}

func TestCapacityFallback_OneContinuation(t *testing.T) {
	for _, tc := range []struct {
		mode   string
		queued bool
		models string
	}{
		{"normal", false, "gpt-6.1-sol"},
		{"capacity", false, "gpt-6.1-sol,gpt-6.1-terra"},
		{"auth", false, "gpt-6.1-sol"},
		{"rate-limit", false, "gpt-6.1-sol"},
		{"tool-error", false, "gpt-6.1-sol"},
		{"fallback-fails", false, "gpt-6.1-sol,gpt-6.1-terra"},
		{"side-effect", false, "gpt-6.1-sol,gpt-6.1-terra"},
		{"capacity", true, "gpt-6.1-sol,gpt-6.1-terra,gpt-6.1-sol"},
	} {
		t.Run(tc.mode+tc.models, func(t *testing.T) {
			p := &stubPlatformEngine{n: "test"}
			e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
			t.Cleanup(func() { _ = e.Stop() })
			key := "test:capacity"
			session := e.sessions.GetOrCreateActive(key)
			as := &capacityTestSession{events: make(chan Event, 16), mode: tc.mode, model: "gpt-6.1-sol"}
			state := &interactiveState{agentSession: as, platform: p, replyCtx: "ctx"}
			if tc.queued {
				state.pendingMessages = []queuedMessage{{platform: p, replyCtx: "queued", content: "next request", messageID: "m2"}}
			}
			if err := as.Send("original request", "m1", []ImageAttachment{{}}, []FileAttachment{{}}); err != nil {
				t.Fatal(err)
			}
			e.processInteractiveEvents(state, session, e.sessions, key, "m1", time.Now(), nil, nil, "ctx", 0)
			as.mu.Lock()
			defer as.mu.Unlock()
			if got := strings.Join(as.models, ","); got != tc.models {
				t.Fatalf("models=%s, want %s; replies=%v", got, tc.models, p.getSent())
			}
			if len(as.models) > 1 {
				if as.fallbacks != 1 || as.prompts[1] == as.prompts[0] || as.attachments[1] != 0 {
					t.Fatalf("fallback replayed input or repeated: prompts=%v attachments=%v count=%d", as.prompts, as.attachments, as.fallbacks)
				}
				if !strings.Contains(strings.Join(p.getSent(), "\n"), "gpt-6.1-sol is at capacity; continuing once with gpt-6.1-terra") {
					t.Fatalf("fallback notice missing: %v", p.getSent())
				}
			}
			if as.model != "gpt-6.1-sol" || (tc.mode == "side-effect" && as.tools != 1) {
				t.Fatalf("model=%s tools=%d", as.model, as.tools)
			}
		})
	}
}
