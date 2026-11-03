package core

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// resumeFailSession simulates an agent session whose --resume target was
// pruned: with failOnSend every Send fails with "No conversation found".
type resumeFailSession struct {
	controllableAgentSession
	failOnSend bool
	gate       chan struct{} // when non-nil, Send blocks until closed before acting

	mu      sync.Mutex // guards sends/prompts: Send runs on the engine's goroutine
	sends   int
	prompts []string
}

func (s *resumeFailSession) Send(prompt string, _ string, _ []ImageAttachment, _ []FileAttachment) error {
	s.mu.Lock()
	s.sends++
	s.prompts = append(s.prompts, prompt)
	s.mu.Unlock()
	if s.gate != nil {
		<-s.gate
	}
	if s.failOnSend {
		s.events <- Event{Type: EventError, Error: errors.New("No conversation found with session ID: 11111111-2222-3333-4444-555555555555")}
		return nil
	}
	s.events <- Event{Type: EventText, Content: "retried answer"}
	s.events <- Event{Type: EventResult, Content: "retried answer", Done: true}
	return nil
}

func (s *resumeFailSession) sendCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sends
}

func (s *resumeFailSession) sentPrompts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.prompts...)
}

func newResumeSession(id string, fail bool) *resumeFailSession {
	return &resumeFailSession{
		controllableAgentSession: controllableAgentSession{
			sessionID: id,
			alive:     true,
			events:    make(chan Event, 8),
			closed:    make(chan struct{}),
		},
		failOnSend: fail,
	}
}

// TestResumeRetryOnMissingConversation is a regression test for tasks dying
// with "No conversation found with session ID" after Claude Code pruned the
// transcript of the stored --resume target (fixed session keys, e.g. cron
// jobs, failed on every run afterwards). The turn must clear the stale
// reference and retry the prompt once in a fresh session, delivering the
// retried answer without surfacing the resume error to the user.
func TestResumeRetryOnMissingConversation(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	first := newResumeSession("pruned-sess", true)
	second := newResumeSession("fresh-sess", false)
	queue := []AgentSession{first, second}
	agent := &controllableAgent{startSessionFn: func(_ context.Context, _ string) (AgentSession, error) {
		s := queue[0]
		queue = queue[1:]
		return s, nil
	}}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	sessionKey := "test:user1"
	session := e.sessions.GetOrCreateActive(sessionKey)
	session.SetAgentSessionID("pruned-session-id", "controllable")
	e.sessions.Save()
	if _, locked := session.TryLock(); !locked {
		t.Fatal("expected session lock")
	}

	e.processInteractiveMessageWith(p, &Message{
		SessionKey: sessionKey,
		UserID:     "user1",
		Content:    "do the task",
		ReplyCtx:   "ctx",
	}, session, agent, e.sessions, sessionKey, "", sessionKey, 0)

	if second.sendCount() != 1 {
		t.Fatalf("expected the prompt to be retried exactly once on a fresh session, got %d sends", second.sendCount())
	}
	if session.GetAgentSessionID() == "pruned-session-id" {
		t.Error("stale AgentSessionID should have been replaced by the fresh session's ID")
	}
	sent := strings.Join(p.getSent(), "\n")
	if !strings.Contains(sent, "retried answer") {
		t.Fatalf("expected the retried answer to be delivered, got %v", p.getSent())
	}
	if strings.Contains(sent, "No conversation found") {
		t.Fatalf("resume error must not be surfaced to the user, got %v", p.getSent())
	}
}

// TestResumeRetryCarriesQueuedMessages: messages the user queued while the
// failing turn was running must survive the recovery — carried over to the
// fresh session and processed there, without a "session reset" error.
func TestResumeRetryCarriesQueuedMessages(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	first := newResumeSession("pruned-sess", true)
	first.gate = make(chan struct{})
	second := newResumeSession("fresh-sess", false)
	queue := []AgentSession{first, second}
	agent := &controllableAgent{startSessionFn: func(_ context.Context, _ string) (AgentSession, error) {
		s := queue[0]
		queue = queue[1:]
		return s, nil
	}}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	sessionKey := "test:user1"
	session := e.sessions.GetOrCreateActive(sessionKey)
	session.SetAgentSessionID("pruned-session-id", "controllable")
	e.sessions.Save()
	if _, locked := session.TryLock(); !locked {
		t.Fatal("expected session lock")
	}

	done := make(chan struct{})
	go func() {
		e.processInteractiveMessageWith(p, &Message{
			SessionKey: sessionKey, UserID: "user1",
			Content:   "do the task",
			ReplyCtx: "ctx",
		}, session, agent, e.sessions, sessionKey, "", sessionKey, 0)
		close(done)
	}()

	// Wait for the failing turn to start, queue a follow-up message behind it.
	deadline := time.Now().Add(2 * time.Second)
	for first.sendCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	e.interactiveMu.Lock()
	st := e.interactiveStates[sessionKey]
	e.interactiveMu.Unlock()
	st.mu.Lock()
	st.pendingMessages = append(st.pendingMessages, queuedMessage{
		platform: p, replyCtx: "ctx-q", content: "queued follow-up",
	})
	st.mu.Unlock()
	close(first.gate) // let the resume error fire

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not complete")
	}

	prompts := second.sentPrompts()
	if second.sendCount() != 2 {
		t.Fatalf("expected the retried prompt plus the carried queued message, got %d sends: %v", second.sendCount(), prompts)
	}
	joined := strings.Join(prompts, "\n")
	if !strings.Contains(joined, "queued follow-up") {
		t.Fatalf("queued message must be carried over to the fresh session, got %v", prompts)
	}
	if strings.Contains(strings.Join(p.getSent(), "\n"), "session reset") {
		t.Fatalf("recovery must not report a session reset to the user, got %v", p.getSent())
	}
}

// TestResumeRetrySecondFailureSurfaces: if the fresh session dies the same
// way, the error must reach the user instead of being swallowed again.
func TestResumeRetrySecondFailureSurfaces(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	first := newResumeSession("pruned-sess", true)
	second := newResumeSession("fresh-sess", true) // fails the same way
	queue := []AgentSession{first, second}
	agent := &controllableAgent{startSessionFn: func(_ context.Context, _ string) (AgentSession, error) {
		s := queue[0]
		queue = queue[1:]
		return s, nil
	}}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	sessionKey := "test:user1"
	session := e.sessions.GetOrCreateActive(sessionKey)
	session.SetAgentSessionID("pruned-session-id", "controllable")
	e.sessions.Save()
	if _, locked := session.TryLock(); !locked {
		t.Fatal("expected session lock")
	}

	e.processInteractiveMessageWith(p, &Message{
		SessionKey: sessionKey, UserID: "user1",
		Content:   "do the task",
		ReplyCtx: "ctx",
	}, session, agent, e.sessions, sessionKey, "", sessionKey, 0)

	if second.sendCount() != 1 {
		t.Fatalf("expected exactly one retry, got %d sends", second.sendCount())
	}
	sent := strings.Join(p.getSent(), "\n")
	if !strings.Contains(sent, "No conversation found") {
		t.Fatalf("the retry's failure must be surfaced to the user, got %v", p.getSent())
	}
}
