//go:build integration

package integration

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Integration tests: unsolicited agent events (background task completion)
//
// Covers the end-to-end flow for events that an agent emits AFTER a user's
// turn has completed — e.g. a Claude Code `run_in_background` bash task
// finishing minutes later. Without the unsolicited reader, those events
// pile up in the buffered channel and get discarded by drainEvents() on
// the next user message.
// ---------------------------------------------------------------------------

// persistentEventsSession is an AgentSession with a long-lived events channel
// that stays open across turns. Unlike FakeAgentSession (which returns a new
// closed channel per Events() call), this is required to model the real
// Claude Code behavior where one channel spans multiple turns.
type persistentEventsSession struct {
	mu        sync.Mutex
	sessionID string
	alive     bool
	events    chan core.Event
	prompts   []string
	closed    chan struct{}
}

func newPersistentEventsSession(id string) *persistentEventsSession {
	return &persistentEventsSession{
		sessionID: id,
		alive:     true,
		events:    make(chan core.Event, 64),
		closed:    make(chan struct{}),
	}
}

func (s *persistentEventsSession) Send(prompt string, _ []core.ImageAttachment, _ []core.FileAttachment) error {
	s.mu.Lock()
	s.prompts = append(s.prompts, prompt)
	s.mu.Unlock()
	return nil
}

func (s *persistentEventsSession) RespondPermission(_ string, _ core.PermissionResult) error {
	return nil
}
func (s *persistentEventsSession) Events() <-chan core.Event { return s.events }
func (s *persistentEventsSession) CurrentSessionID() string  { return s.sessionID }
func (s *persistentEventsSession) Alive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.alive
}
func (s *persistentEventsSession) Close() error {
	s.mu.Lock()
	if !s.alive {
		s.mu.Unlock()
		return nil
	}
	s.alive = false
	close(s.events)
	s.mu.Unlock()
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return nil
}

// emit pushes an event into the channel.
func (s *persistentEventsSession) emit(ev core.Event) {
	s.events <- ev
}

// promptCount returns how many Send calls have occurred (indicates how many
// foreground turns the engine has dispatched).
func (s *persistentEventsSession) promptCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.prompts)
}

type persistentEventsAgent struct {
	name    string
	session *persistentEventsSession
}

func newPersistentEventsAgent(name string, session *persistentEventsSession) *persistentEventsAgent {
	return &persistentEventsAgent{name: name, session: session}
}

func (a *persistentEventsAgent) Name() string { return a.name }
func (a *persistentEventsAgent) StartSession(_ context.Context, _ string) (core.AgentSession, error) {
	return a.session, nil
}
func (a *persistentEventsAgent) ListSessions(_ context.Context) ([]core.AgentSessionInfo, error) {
	return nil, nil
}
func (a *persistentEventsAgent) Stop() error { return nil }

// capturingPlatform records all messages sent via Send/Reply and captures
// the handler passed to Start so tests can inject messages directly.
type capturingPlatform struct {
	mu      sync.Mutex
	sent    []string
	handler core.MessageHandler
	started chan struct{}
}

func newCapturingPlatform() *capturingPlatform {
	return &capturingPlatform{started: make(chan struct{})}
}

func (p *capturingPlatform) Name() string { return "capture" }
func (p *capturingPlatform) Start(h core.MessageHandler) error {
	p.mu.Lock()
	p.handler = h
	p.mu.Unlock()
	select {
	case <-p.started:
	default:
		close(p.started)
	}
	return nil
}
func (p *capturingPlatform) Reply(_ context.Context, _ any, c string) error {
	p.mu.Lock()
	p.sent = append(p.sent, c)
	p.mu.Unlock()
	return nil
}
func (p *capturingPlatform) Send(_ context.Context, _ any, c string) error {
	p.mu.Lock()
	p.sent = append(p.sent, c)
	p.mu.Unlock()
	return nil
}
func (p *capturingPlatform) Stop() error { return nil }

func (p *capturingPlatform) messages() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]string, len(p.sent))
	copy(cp, p.sent)
	return cp
}

func (p *capturingPlatform) dispatch(msg *core.Message) {
	p.mu.Lock()
	h := p.handler
	p.mu.Unlock()
	if h == nil {
		panic("platform handler not set — Start must be called first")
	}
	h(p, msg)
}

// waitForMessage polls until a message containing substr is relayed or
// the timeout expires. Returns true if found. Event-driven, no fixed sleeps.
func waitForMessage(t *testing.T, p *capturingPlatform, substr string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, m := range p.messages() {
			if strings.Contains(m, substr) {
				return true
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// waitForPromptCount polls until the session has received at least n prompts
// (i.e. n foreground turns have been dispatched).
func waitForPromptCount(t *testing.T, s *persistentEventsSession, n int, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s.promptCount() >= n {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// TestIntegration_UnsolicitedEventsEndToEnd verifies the full happy-path:
//  1. User sends msg → agent completes turn → foreground response delivered
//  2. Agent later emits new events (simulating background task completion)
//     → unsolicited reader relays them to the platform
//  3. User sends a SECOND msg → unsolicited events are NOT re-delivered and
//     are NOT drained (eventsNeedResync=false after the clean unsolicited
//     turn), and the new foreground response is delivered correctly
func TestIntegration_UnsolicitedEventsEndToEnd(t *testing.T) {
	sess := newPersistentEventsSession("unsol-e2e")
	agent := newPersistentEventsAgent("fake-claude", sess)
	platform := newCapturingPlatform()

	engine := core.NewEngine("test", agent, []core.Platform{platform}, "", core.LangEnglish)
	defer engine.Stop()
	require.NoError(t, engine.Start())

	// Platform.Start must have been called by engine — handler is now available.
	<-platform.started

	// ─── Phase 1: user sends first message ──────────────────────
	userMsg1 := &core.Message{
		SessionKey: "test:ch1:u1",
		Platform:   "capture",
		MessageID:  "msg-001",
		UserID:     "u1",
		Content:    "run 5 campaigns in background",
		ReplyCtx:   "rctx-1",
	}
	go platform.dispatch(userMsg1)

	// Synchronize on the agent receiving the prompt (not on wall-clock time).
	require.True(t, waitForPromptCount(t, sess, 1, 3*time.Second),
		"agent never received the first prompt")

	// Feed the foreground turn events.
	sess.emit(core.Event{Type: core.EventText, Content: "Submitted, waiting..."})
	sess.emit(core.Event{Type: core.EventResult, Content: "Submitted, waiting..."})

	require.True(t, waitForMessage(t, platform, "Submitted", 3*time.Second),
		"foreground turn response was not delivered")

	// ─── Phase 2: simulate background task completion ──────────
	// These events arrive AFTER the foreground turn ended. Under the old
	// behavior they would sit in the buffer until drained by the next msg.
	const bgDoneMarker = "All 5 campaigns created successfully"
	sess.emit(core.Event{Type: core.EventText, Content: bgDoneMarker})
	sess.emit(core.Event{Type: core.EventResult, Content: bgDoneMarker})

	require.True(t, waitForMessage(t, platform, bgDoneMarker, 3*time.Second),
		"unsolicited event was not relayed to platform")

	// ─── Phase 3: user sends a SECOND message ──────────────────
	// This exercises the conditional-drain path: eventsNeedResync is false
	// (clean unsolicited turn), so any events here must be attributed to
	// the new turn rather than drained away. Since the channel is empty
	// by now, this is really a smoke test that a follow-up turn still works.
	userMsg2 := &core.Message{
		SessionKey: "test:ch1:u1",
		Platform:   "capture",
		MessageID:  "msg-002",
		UserID:     "u1",
		Content:    "thanks, any failures?",
		ReplyCtx:   "rctx-2",
	}
	go platform.dispatch(userMsg2)

	require.True(t, waitForPromptCount(t, sess, 2, 3*time.Second),
		"second foreground turn never dispatched to agent")

	const secondTurnMarker = "no failures, all clean"
	sess.emit(core.Event{Type: core.EventText, Content: secondTurnMarker})
	sess.emit(core.Event{Type: core.EventResult, Content: secondTurnMarker})

	require.True(t, waitForMessage(t, platform, secondTurnMarker, 3*time.Second),
		"second foreground turn response was not delivered")

	// Final sanity: all three distinct messages present in order.
	msgs := platform.messages()
	t.Logf("platform received %d messages: %v", len(msgs), msgs)
	assert.True(t, containsAll(msgs, "Submitted", bgDoneMarker, secondTurnMarker),
		"expected all three messages to be delivered: %v", msgs)
}

// TestIntegration_StaleEventsDrainedAfterAbnormalExit verifies that when a
// turn ends abnormally (EventError → eventsNeedResync=true), any events that
// arrive afterward are NOT relayed as unsolicited AND are drained (not
// mistaken for the response of) when the next user message starts a new turn.
func TestIntegration_StaleEventsDrainedAfterAbnormalExit(t *testing.T) {
	sess := newPersistentEventsSession("unsol-abnormal")
	agent := newPersistentEventsAgent("fake-claude", sess)
	platform := newCapturingPlatform()

	engine := core.NewEngine("test", agent, []core.Platform{platform}, "", core.LangEnglish)
	defer engine.Stop()
	require.NoError(t, engine.Start())
	<-platform.started

	// ─── Phase 1: user message → abnormal exit ──────────────────
	userMsg1 := &core.Message{
		SessionKey: "test:ch1:u1",
		Platform:   "capture",
		MessageID:  "msg-err",
		UserID:     "u1",
		Content:    "do something",
		ReplyCtx:   "rctx",
	}
	go platform.dispatch(userMsg1)

	require.True(t, waitForPromptCount(t, sess, 1, 3*time.Second),
		"agent never received the prompt")

	// Turn exits abnormally — EventError sets eventsNeedResync=true and
	// causes cleanupInteractiveState in the EventError path (if agent is
	// reported dead). Here the agent is still Alive(), so session persists
	// but the flag stays true.
	sess.emit(core.Event{Type: core.EventError, Error: simpleError("turn failed")})

	require.True(t, waitForMessage(t, platform, "turn failed", 3*time.Second),
		"error message not delivered")

	// ─── Phase 2: push "leftover" events that would wrongly relay ──
	// Because eventsNeedResync=true after the error, the unsolicited reader
	// should NOT be started. These events should sit in the buffer.
	const leftoverMarker = "LEFTOVER-SHOULD-BE-DRAINED"
	sess.emit(core.Event{Type: core.EventText, Content: leftoverMarker})
	sess.emit(core.Event{Type: core.EventResult, Content: leftoverMarker})

	// ─── Phase 3: send a NEXT user message ─────────────────────
	// drainEvents() in processInteractiveMessageWith should clear the
	// buffered leftovers BEFORE agent.Send() is called for the new turn.
	userMsg2 := &core.Message{
		SessionKey: "test:ch1:u1",
		Platform:   "capture",
		MessageID:  "msg-002",
		UserID:     "u1",
		Content:    "retry please",
		ReplyCtx:   "rctx-2",
	}
	go platform.dispatch(userMsg2)

	require.True(t, waitForPromptCount(t, sess, 2, 3*time.Second),
		"second foreground turn never dispatched to agent")

	// Feed the new turn's events.
	const retryMarker = "retry succeeded"
	sess.emit(core.Event{Type: core.EventText, Content: retryMarker})
	sess.emit(core.Event{Type: core.EventResult, Content: retryMarker})

	require.True(t, waitForMessage(t, platform, retryMarker, 3*time.Second),
		"retry turn response not delivered")

	// ─── Verify: leftovers were NEVER relayed, before or after the retry ──
	for _, m := range platform.messages() {
		if strings.Contains(m, leftoverMarker) {
			t.Fatalf("leftover event from abnormal turn was incorrectly relayed: %v",
				platform.messages())
		}
	}
}

func containsAll(msgs []string, needles ...string) bool {
	joined := strings.Join(msgs, "\n")
	for _, n := range needles {
		if !strings.Contains(joined, n) {
			return false
		}
	}
	return true
}

type simpleError string

func (e simpleError) Error() string { return string(e) }
