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

// scriptedRetrySession answers each Send with the next step of its script:
// "fail" is an immediate retriable error, "progress-fail" runs a tool and then
// hits a retriable error, anything else completes the turn.
type scriptedRetrySession struct {
	sessionID string
	events    chan Event
	script    []string
	mu        sync.Mutex
	sends     int
	prompts   []string
	images    []int
}

func newScriptedRetrySession(id string, script ...string) *scriptedRetrySession {
	return &scriptedRetrySession{sessionID: id, events: make(chan Event, 16), script: script}
}

func (s *scriptedRetrySession) Send(prompt string, _ string, images []ImageAttachment, _ []FileAttachment) error {
	s.mu.Lock()
	step := "ok"
	if s.sends < len(s.script) {
		step = s.script[s.sends]
	}
	s.sends++
	s.prompts = append(s.prompts, prompt)
	s.images = append(s.images, len(images))
	s.mu.Unlock()
	overloaded := Event{Type: EventError, Error: errors.New("APIError: Our servers are currently overloaded. Please try again later."), ErrorKind: ErrorKindOverloaded}
	switch step {
	case "fail":
		s.events <- overloaded
	case "text-fail":
		s.events <- Event{Type: EventText, Content: "partial answer"}
		s.events <- overloaded
	case "progress-fail":
		s.events <- Event{Type: EventToolUse, ToolName: "shell", ToolInput: "echo hi"}
		s.events <- Event{Type: EventToolResult, ToolName: "shell", ToolResult: "hi"}
		s.events <- overloaded
	default:
		s.events <- Event{Type: EventResult, Content: "ok after retry", Done: true}
	}
	return nil
}

func (s *scriptedRetrySession) RespondPermission(_ string, _ PermissionResult) error { return nil }
func (s *scriptedRetrySession) Events() <-chan Event                                 { return s.events }
func (s *scriptedRetrySession) CurrentSessionID() string                             { return s.sessionID }
func (s *scriptedRetrySession) Alive() bool                                          { return true }
func (s *scriptedRetrySession) Close() error                                         { close(s.events); return nil }
func (s *scriptedRetrySession) sendCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sends
}

// retryNoticesIn returns the distinct "(retry N/M)" markers of a card's final
// content in order of appearance.
func retryNoticesIn(content string) []string {
	var got []string
	for _, part := range strings.Split(content, "(retry ")[1:] {
		if end := strings.Index(part, ")"); end > 0 {
			got = append(got, part[:end])
		}
	}
	return got
}

// The first failure is the first retry, and every retry adds its own entry to
// the card instead of only the first and every fifth one.
func TestProcessInteractiveTurnWithRetry_EveryRetryIsANumberedCardEntry(t *testing.T) {
	shortRetryPolicy(t, 5)
	p := &retryCardTestPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	sessionKey := "test:user1"
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := newScriptedRetrySession("s1", "fail", "fail", "fail")
	state := &interactiveState{agentSession: agentSession, platform: p, replyCtx: "ctx-1", eventsNeedResync: true}
	e.interactiveStates[sessionKey] = state

	e.processInteractiveTurnWithRetry(state, session, e.sessions, sessionKey, "hello", "m1", nil, nil, "ctx-1", time.Now(), sessionKey, len("hello"), 0)

	if got := agentSession.sendCount(); got != 4 {
		t.Fatalf("sendCount = %d, want 4 (three retries)", got)
	}
	_, finalized := p.cards[0].snapshot()
	if len(finalized) != 1 {
		t.Fatalf("finalized = %q, want the card closed once", finalized)
	}
	got := strings.Join(retryNoticesIn(finalized[0]), ",")
	if got != "1/4,2/4,3/4" {
		t.Fatalf("retry entries = %q, want 1/4,2/4,3/4 in %q", got, finalized[0])
	}
}

// An attempt that produced output before failing proves nothing about the
// upstream recovering: the failure came after the output. The retry budget is
// therefore per turn and is never reset, so a turn that keeps failing after
// producing output still gives up instead of looping forever.
func TestProcessInteractiveTurnWithRetry_ProgressDoesNotResetTheBudget(t *testing.T) {
	shortRetryPolicy(t, 2)
	p := &retryCardTestPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	agent := &continueStubAgent{prompt: "CONTINUE-FROM-HERE"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	sessionKey := "test:user1"
	session := e.sessions.GetOrCreateActive(sessionKey)
	// Every attempt produces text and then fails retriably: exactly the shape
	// the old reset treated as "progress" and looped on.
	agentSession := newScriptedRetrySession("s1", "text-fail", "text-fail", "text-fail", "text-fail")
	state := &interactiveState{agentSession: agentSession, platform: p, replyCtx: "ctx-1", eventsNeedResync: true}
	e.interactiveStates[sessionKey] = state

	done := make(chan struct{})
	go func() {
		e.processInteractiveTurnWithRetry(state, session, e.sessions, sessionKey, "hello", "m1", nil, nil, "ctx-1", time.Now(), sessionKey, len("hello"), 0)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("retry loop did not stop: the retry budget was reset by an attempt that produced output")
	}

	if got := agentSession.sendCount(); got != 2 {
		t.Fatalf("sendCount = %d, want 2 (max_attempts = 2 is the whole turn's budget)", got)
	}
	var failed bool
	for _, msg := range p.getSent() {
		if strings.Contains(msg, "overloaded") {
			failed = true
		}
	}
	if !failed {
		t.Fatalf("sent = %q, want the turn to end with the retriable error", p.getSent())
	}
}

// Without a card the notice is a separate chat message, so it stays throttled
// to the first retry and every fifth one.
func TestProcessInteractiveTurnWithRetry_MessageNoticesStayThrottled(t *testing.T) {
	shortRetryPolicy(t, 6)
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	sessionKey := "test:user1"
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := newScriptedRetrySession("s1", "fail", "fail", "fail", "fail", "fail")
	state := &interactiveState{agentSession: agentSession, platform: p, replyCtx: "ctx-1", eventsNeedResync: true}
	e.interactiveStates[sessionKey] = state

	e.processInteractiveTurnWithRetry(state, session, e.sessions, sessionKey, "hello", "m1", nil, nil, "ctx-1", time.Now(), sessionKey, len("hello"), 0)

	var notices []string
	for _, msg := range p.getSent() {
		notices = append(notices, retryNoticesIn(msg)...)
	}
	if got := strings.Join(notices, ","); got != "1/5,5/5" {
		t.Fatalf("message notices = %q, want 1/5,5/5; sent = %q", got, p.getSent())
	}
}

func (s *scriptedRetrySession) sent() (prompts []string, images []int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.prompts...), append([]int(nil), s.images...)
}

// promptKeptStubAgent can resume a turn and records the prompt in its
// conversation before a retriable error can happen (as OpenCode does).
type promptKeptStubAgent struct {
	continueStubAgent
}

func (a *promptKeptStubAgent) PromptKeptOnRetriableError() bool { return true }

// An agent that already recorded the prompt when the error arrived is asked to
// continue, even though the failed attempt produced nothing: replaying would
// add the prompt and its images to the conversation once more per retry.
func TestProcessInteractiveTurnWithRetry_KeptPromptIsContinuedNotReplayed(t *testing.T) {
	shortRetryPolicy(t, 5)
	p := &stubPlatformEngine{n: "test"}
	agent := &promptKeptStubAgent{continueStubAgent{prompt: "CONTINUE-FROM-HERE"}}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	sessionKey := "test:user1"
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := newScriptedRetrySession("s1", "fail", "fail")
	state := &interactiveState{agentSession: agentSession, platform: p, replyCtx: "ctx-1", eventsNeedResync: true}
	e.interactiveStates[sessionKey] = state
	images := []ImageAttachment{{MimeType: "image/jpeg", Data: []byte("jpeg")}}

	e.processInteractiveTurnWithRetry(state, session, e.sessions, sessionKey, "look at this", "m1", images, nil, "ctx-1", time.Now(), sessionKey, len("look at this"), 0)

	prompts, imageCounts := agentSession.sent()
	if strings.Join(prompts, "|") != "look at this|CONTINUE-FROM-HERE|CONTINUE-FROM-HERE" {
		t.Fatalf("prompts = %q, want the original prompt once, then continue prompts", prompts)
	}
	if imageCounts[0] != 1 || imageCounts[1] != 0 || imageCounts[2] != 0 {
		t.Fatalf("images per send = %v, want the images only with the original prompt", imageCounts)
	}
	var notices []string
	for _, msg := range p.getSent() {
		notices = append(notices, retryNoticesIn(msg)...)
	}
	// Failures without progress keep counting: continuing is not a success.
	if got := strings.Join(notices, ","); got != "1/4" {
		t.Fatalf("message notices = %q, want 1/4 (the second retry is throttled, not reset)", got)
	}
}

// An agent that can resume but does not promise the prompt was recorded keeps
// the replay: continuing would lose a prompt the conversation never saw.
func TestProcessInteractiveTurnWithRetry_ResumableAgentStillReplaysUnkeptPrompt(t *testing.T) {
	shortRetryPolicy(t, 3)
	p := &stubPlatformEngine{n: "test"}
	agent := &continueStubAgent{prompt: "CONTINUE-FROM-HERE"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	sessionKey := "test:user1"
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := newScriptedRetrySession("s1", "fail")
	state := &interactiveState{agentSession: agentSession, platform: p, replyCtx: "ctx-1", eventsNeedResync: true}
	e.interactiveStates[sessionKey] = state

	e.processInteractiveTurnWithRetry(state, session, e.sessions, sessionKey, "hello", "m1", nil, nil, "ctx-1", time.Now(), sessionKey, len("hello"), 0)

	if prompts, _ := agentSession.sent(); strings.Join(prompts, "|") != "hello|hello" {
		t.Fatalf("prompts = %q, want the original prompt replayed", prompts)
	}
}

// previewEventLog records the ordering of the events a preview-only platform
// sees (a send vs. a preview start/delete), so a test can tell that the failed
// attempt's preview is gone before the retry sends again.
type previewEventLog struct {
	mu  sync.Mutex
	seq []string
}

func (l *previewEventLog) add(entry string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq = append(l.seq, entry)
}

func (l *previewEventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.seq...)
}

// previewRetryPlatform is the stream-preview path used by Discord/Telegram:
// it updates messages and deletes previews, but has no streaming cards.
type previewRetryPlatform struct {
	stubPlatformEngine
	log *previewEventLog
}

func (p *previewRetryPlatform) SendPreviewStart(_ context.Context, _ any, _ string) (any, error) {
	p.log.add("start")
	return "preview-handle", nil
}

func (p *previewRetryPlatform) UpdateMessage(_ context.Context, _ any, _ string) error {
	p.log.add("update")
	return nil
}

func (p *previewRetryPlatform) DeletePreviewMessage(_ context.Context, _ any) error {
	p.log.add("delete")
	return nil
}

type previewRetrySession struct {
	sessionID string
	events    chan Event
	log       *previewEventLog
	mu        sync.Mutex
	sends     int
}

func (s *previewRetrySession) Send(_ string, _ string, _ []ImageAttachment, _ []FileAttachment) error {
	s.log.add("send")
	s.mu.Lock()
	s.sends++
	sendNo := s.sends
	s.mu.Unlock()
	if sendNo == 1 {
		s.events <- Event{Type: EventText, Content: "partial answer"}
		s.events <- Event{Type: EventError, Error: errors.New("APIError: Our servers are currently overloaded. Please try again later."), ErrorKind: ErrorKindOverloaded}
		return nil
	}
	s.events <- Event{Type: EventText, Content: "final answer"}
	s.events <- Event{Type: EventResult, Content: "final answer", Done: true}
	return nil
}

func (s *previewRetrySession) RespondPermission(_ string, _ PermissionResult) error { return nil }
func (s *previewRetrySession) Events() <-chan Event                                 { return s.events }
func (s *previewRetrySession) CurrentSessionID() string                             { return s.sessionID }
func (s *previewRetrySession) Alive() bool                                          { return true }
func (s *previewRetrySession) Close() error                                         { close(s.events); return nil }

// A failed attempt's stream preview must be discarded before the retry starts:
// every attempt opens its own preview, so leaving the dead one behind leaves a
// stale partial answer in the chat for each failed attempt.
func TestProcessInteractiveTurnWithRetry_DiscardsFailedAttemptPreview(t *testing.T) {
	shortRetryPolicy(t, 3)
	log := &previewEventLog{}
	p := &previewRetryPlatform{stubPlatformEngine: stubPlatformEngine{n: "discord"}, log: log}
	e := NewEngine("test", &continueStubAgent{prompt: "CONTINUE-FROM-HERE"}, []Platform{p}, "", LangEnglish)
	e.SetStreamPreviewCfg(StreamPreviewCfg{Enabled: true, MinDeltaChars: 1, MaxChars: 500})
	sessionKey := "test:user1"
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := &previewRetrySession{sessionID: "s1", events: make(chan Event, 16), log: log}
	state := &interactiveState{agentSession: agentSession, platform: p, replyCtx: "ctx-1", eventsNeedResync: true}
	e.interactiveStates[sessionKey] = state

	e.processInteractiveTurnWithRetry(state, session, e.sessions, sessionKey, "hello", "m1", nil, nil, "ctx-1", time.Now(), sessionKey, len("hello"), 0)

	var sends, deletes []int
	for i, entry := range log.snapshot() {
		switch entry {
		case "send":
			sends = append(sends, i)
		case "delete":
			deletes = append(deletes, i)
		}
	}
	if len(sends) != 2 {
		t.Fatalf("sends = %d, want 2 (one retried turn); log = %v", len(sends), log.snapshot())
	}
	if len(deletes) == 0 {
		t.Fatalf("no preview was deleted; log = %v", log.snapshot())
	}
	discarded := false
	for _, d := range deletes {
		if d > sends[0] && d < sends[1] {
			discarded = true
			break
		}
	}
	if !discarded {
		t.Fatalf("the failed attempt's preview was not deleted before the retry; log = %v", log.snapshot())
	}
}
