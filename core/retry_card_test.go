package core

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// retryCardTestPlatform counts the streaming cards a turn opens and
// keeps the last one, so a test can tell one card per turn from one per attempt.
type retryCardTestPlatform struct {
	stubPlatformEngine
	mu      sync.Mutex
	creates int
	cards   []*retryTestCard
}

func (p *retryCardTestPlatform) CreateStreamingCard(_ context.Context, _ any) (StreamingCard, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.creates++
	c := &retryTestCard{}
	p.cards = append(p.cards, c)
	return c, nil
}

func (p *retryCardTestPlatform) cardCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.creates
}

type retryTestCard struct {
	mu        sync.Mutex
	updates   []string
	finalized []string
}

func (c *retryTestCard) Update(_ context.Context, content string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.updates = append(c.updates, content)
	return nil
}

func (c *retryTestCard) Finalize(_ context.Context, content string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.finalized = append(c.finalized, content)
	return nil
}

func (c *retryTestCard) Failed() bool { return false }

// SupportsStreamingCardPayload makes the card render structured panels (as the
// Feishu card does) where the platform offers them, so tool-panel entries keep
// their text; it is a harmless extra method where panels do not exist.
func (c *retryTestCard) SupportsStreamingCardPayload() bool { return true }

func (c *retryTestCard) snapshot() (updates, finalized []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.updates...), append([]string(nil), c.finalized...)
}

// continueStubAgent is a stubAgent that can resume an interrupted turn.
type continueStubAgent struct {
	stubAgent
	prompt string
}

func (a *continueStubAgent) ContinueAfterErrorPrompt() string { return a.prompt }

// promptRecordingSideEffectSession runs a tool and then hits a retriable error on
// the first send, and answers on the next one; it records every prompt.
type promptRecordingSideEffectSession struct {
	sessionID string
	events    chan Event
	mu        sync.Mutex
	prompts   []string
}

func (s *promptRecordingSideEffectSession) Send(prompt string, _ string, _ []ImageAttachment, _ []FileAttachment) error {
	s.mu.Lock()
	s.prompts = append(s.prompts, prompt)
	sendNo := len(s.prompts)
	s.mu.Unlock()
	if sendNo == 1 {
		s.events <- Event{Type: EventToolUse, ToolName: "shell", ToolInput: "git push"}
		s.events <- Event{Type: EventToolResult, ToolName: "shell", ToolResult: "pushed"}
		s.events <- Event{Type: EventError, Error: errors.New("APIError: Our servers are currently overloaded. Please try again later."), ErrorKind: ErrorKindOverloaded}
		return nil
	}
	s.events <- Event{Type: EventResult, Content: "done after continue", Done: true}
	return nil
}

func (s *promptRecordingSideEffectSession) RespondPermission(_ string, _ PermissionResult) error {
	return nil
}
func (s *promptRecordingSideEffectSession) Events() <-chan Event     { return s.events }
func (s *promptRecordingSideEffectSession) CurrentSessionID() string { return s.sessionID }
func (s *promptRecordingSideEffectSession) Alive() bool              { return true }
func (s *promptRecordingSideEffectSession) Close() error             { close(s.events); return nil }
func (s *promptRecordingSideEffectSession) sentPrompts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.prompts...)
}

func shortRetryPolicy(t *testing.T, maxAttempts int) {
	t.Helper()
	oldInitialDelay := RetriableErrorInitialDelay
	oldRetryDelay := RetriableErrorRetryDelay
	oldMaxAttempts := RetriableErrorMaxAttempts
	RetriableErrorInitialDelay = time.Millisecond
	RetriableErrorRetryDelay = time.Millisecond
	RetriableErrorMaxAttempts = maxAttempts
	t.Cleanup(func() {
		RetriableErrorInitialDelay = oldInitialDelay
		RetriableErrorRetryDelay = oldRetryDelay
		RetriableErrorMaxAttempts = oldMaxAttempts
	})
}

// A retried turn stays in one streaming card: the retry notice is an entry of
// that card's tool panel and the retry attempt keeps updating the same card,
// instead of every attempt opening a card of its own.
func TestProcessInteractiveTurnWithRetry_RetryStaysInOneStreamingCard(t *testing.T) {
	shortRetryPolicy(t, 3)
	p := &retryCardTestPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	sessionKey := "test:user1"
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := newRetryOnceAgentSession("s1")
	state := &interactiveState{agentSession: agentSession, platform: p, replyCtx: "ctx-1", eventsNeedResync: true}
	e.interactiveStates[sessionKey] = state

	e.processInteractiveTurnWithRetry(state, session, e.sessions, sessionKey, "hello", "m1", nil, nil, "ctx-1", time.Now(), sessionKey, len("hello"), 0)

	if got := agentSession.sendCount(); got != 2 {
		t.Fatalf("sendCount = %d, want 2 (one retry)", got)
	}
	if got := p.cardCount(); got != 1 {
		t.Fatalf("streaming cards = %d, want 1: the retry must continue in the first attempt's card", got)
	}
	updates, finalized := p.cards[0].snapshot()
	var noticed bool
	for _, u := range updates {
		if strings.Contains(u, "自动重试") && strings.Contains(u, "Retrying") {
			noticed = true
		}
	}
	if !noticed {
		t.Fatalf("card updates = %q, want the retry notice as a tool entry of the card", updates)
	}
	if len(finalized) != 1 {
		t.Fatalf("finalized = %q, want the card closed exactly once", finalized)
	}
	// Depending on the card design the answer is part of the card or sent right
	// after it (panel cards keep the process, the answer follows as a message).
	if !strings.Contains(finalized[0]+strings.Join(p.getSent(), "\n"), "ok after retry") {
		t.Fatalf("finalized = %q, sent = %q, want the retried answer delivered", finalized, p.getSent())
	}
	if strings.Contains(strings.Join(finalized, ""), "Retrying") == false {
		t.Fatalf("final card %q lost the retry notice entry", finalized)
	}
	for _, msg := range p.getSent() {
		if strings.Contains(msg, "Retrying") {
			t.Fatalf("sent %q as a separate message; the notice belongs in the card", msg)
		}
	}
	if state.retryCarry != nil {
		t.Fatal("retry card hand-off left behind after the turn")
	}
}

// A retriable error after the turn already ran tools must not replay the prompt
// (the push would run again). An agent that keeps the conversation is asked to
// continue instead, and the turn completes.
func TestProcessInteractiveTurnWithRetry_ContinuesAfterSideEffectsWhenAgentCanResume(t *testing.T) {
	shortRetryPolicy(t, 3)
	p := &stubPlatformEngine{n: "test"}
	agent := &continueStubAgent{prompt: "CONTINUE-FROM-HERE"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	sessionKey := "test:user1"
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := &promptRecordingSideEffectSession{sessionID: "s1", events: make(chan Event, 8)}
	state := &interactiveState{agentSession: agentSession, platform: p, replyCtx: "ctx-1", eventsNeedResync: true}
	e.interactiveStates[sessionKey] = state

	e.processInteractiveTurnWithRetry(state, session, e.sessions, sessionKey, "push it", "m1", nil, nil, "ctx-1", time.Now(), sessionKey, len("push it"), 0)

	prompts := agentSession.sentPrompts()
	if len(prompts) != 2 || prompts[0] != "push it" || prompts[1] != "CONTINUE-FROM-HERE" {
		t.Fatalf("prompts = %q, want the original prompt then the continue prompt (never a replay)", prompts)
	}
	sent := p.getSent()
	var answered bool
	for _, msg := range sent {
		if strings.Contains(msg, "overloaded") && !strings.Contains(msg, "Retrying") {
			t.Fatalf("sent %q: the error must not be reported when the turn continues", msg)
		}
		if strings.Contains(msg, "done after continue") {
			answered = true
		}
	}
	if !answered {
		t.Fatalf("sent = %q, want the continued answer", sent)
	}
}

// Without resume support the old conservative behaviour stays: no replay, the
// error is reported.
func TestProcessInteractiveTurnWithRetry_SideEffectsWithoutResumeStillFail(t *testing.T) {
	shortRetryPolicy(t, 3)
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	sessionKey := "test:user1"
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := &promptRecordingSideEffectSession{sessionID: "s1", events: make(chan Event, 8)}
	state := &interactiveState{agentSession: agentSession, platform: p, replyCtx: "ctx-1", eventsNeedResync: true}
	e.interactiveStates[sessionKey] = state

	e.processInteractiveTurnWithRetry(state, session, e.sessions, sessionKey, "push it", "m1", nil, nil, "ctx-1", time.Now(), sessionKey, len("push it"), 0)

	if got := agentSession.sentPrompts(); len(got) != 1 {
		t.Fatalf("prompts = %q, want no replay and no continue without resume support", got)
	}
	var reported bool
	for _, msg := range p.getSent() {
		if strings.Contains(msg, "overloaded") {
			reported = true
		}
	}
	if !reported {
		t.Fatalf("sent = %q, want the error reported", p.getSent())
	}
}

// Stopping during the retry delay closes the card instead of leaving it open or
// handing it to an unrelated next turn.
func TestProcessInteractiveTurnWithRetry_StopDuringRetryClosesTheCard(t *testing.T) {
	oldInitialDelay := RetriableErrorInitialDelay
	oldRetryDelay := RetriableErrorRetryDelay
	oldMaxAttempts := RetriableErrorMaxAttempts
	RetriableErrorInitialDelay = time.Hour
	RetriableErrorRetryDelay = time.Hour
	RetriableErrorMaxAttempts = 3
	t.Cleanup(func() {
		RetriableErrorInitialDelay = oldInitialDelay
		RetriableErrorRetryDelay = oldRetryDelay
		RetriableErrorMaxAttempts = oldMaxAttempts
	})

	p := &retryCardTestPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	sessionKey := "test:user1"
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := newAlwaysRetryAgentSession("s1")
	state := &interactiveState{agentSession: agentSession, platform: p, replyCtx: "ctx-1"}
	e.interactiveStates[sessionKey] = state

	done := make(chan struct{})
	go func() {
		e.processInteractiveTurnWithRetry(state, session, e.sessions, sessionKey, "hello", "m1", nil, nil, "ctx-1", time.Now(), sessionKey, len("hello"), 0)
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for agentSession.sendCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	state.markStopped()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("retry delay did not stop")
	}
	if p.cardCount() != 1 {
		t.Fatalf("cards = %d, want 1", p.cardCount())
	}
	if _, finalized := p.cards[0].snapshot(); len(finalized) != 1 {
		t.Fatalf("finalized = %d times, want the card closed once on stop", len(finalized))
	}
	if state.retryCarry != nil {
		t.Fatal("retry card hand-off leaked past a stopped turn")
	}
}
