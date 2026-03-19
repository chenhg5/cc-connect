package fake

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// FakeAgentSession is a fake implementation of AgentSession for testing.
// It simulates agent behavior without calling real CLI tools.
type FakeAgentSession struct {
	mu            sync.RWMutex
	sessionID     string
	promptQueue   []string
	events        []core.Event
	closed        bool
	alive         bool
	responseDelay time.Duration
	responses     []string
	responseIdx   int
}

func NewFakeAgentSession(sessionID string) *FakeAgentSession {
	return &FakeAgentSession{
		sessionID: sessionID,
		alive:     true,
		events:    make([]core.Event, 0),
		promptQueue: make([]string, 0),
	}
}

// SetResponseDelay sets a delay before sending responses (for timeout testing).
func (s *FakeAgentSession) SetResponseDelay(delay time.Duration) *FakeAgentSession {
	s.responseDelay = delay
	return s
}

// SetResponses sets predefined responses to return.
func (s *FakeAgentSession) SetResponses(responses ...string) *FakeAgentSession {
	s.responses = responses
	return s
}

// AddTextEvent adds a text event to the event stream.
func (s *FakeAgentSession) AddTextEvent(content string) *FakeAgentSession {
	s.events = append(s.events, TestTextEvent(content))
	return s
}

// AddResultEvent adds a result event to the event stream.
func (s *FakeAgentSession) AddResultEvent(content string) *FakeAgentSession {
	s.events = append(s.events, TestResultEvent(content))
	return s
}

// AddErrorEvent adds an error event to the event stream.
func (s *FakeAgentSession) AddErrorEvent(err error) *FakeAgentSession {
	s.events = append(s.events, TestErrorEvent(err))
	return s
}

// AddThinkingEvent adds a thinking event to the event stream.
func (s *FakeAgentSession) AddThinkingEvent(content string) *FakeAgentSession {
	s.events = append(s.events, TestThinkingEvent(content))
	return s
}

// AddPermissionRequest adds a permission request event.
func (s *FakeAgentSession) AddPermissionRequest(requestID, toolName, toolInput string) *FakeAgentSession {
	s.events = append(s.events, TestPermissionRequestEvent(requestID, toolName, toolInput))
	return s
}

func (s *FakeAgentSession) Send(prompt string, images []core.ImageAttachment, files []core.FileAttachment) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return io.ErrClosedPipe
	}

	s.promptQueue = append(s.promptQueue, prompt)

	if len(s.events) == 0 {
		if len(s.responses) > 0 && s.responseIdx < len(s.responses) {
			resp := s.responses[s.responseIdx]
			s.responseIdx++
			s.events = append(s.events, TestTextEvent(resp), TestResultEvent(resp))
		} else {
			s.events = append(s.events, TestTextEvent("Processing: "+prompt), TestResultEvent("Done"))
		}
	}
	delay := s.responseDelay
	s.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}

	return nil
}

func (s *FakeAgentSession) RespondPermission(requestID string, result core.PermissionResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, core.Event{
		Type:    core.EventToolResult,
		Content: "Permission " + result.Behavior,
	})
	return nil
}

func (s *FakeAgentSession) Events() <-chan core.Event {
	s.mu.RLock()
	defer s.mu.RUnlock()

	needsDone := len(s.events) == 0 || !s.events[len(s.events)-1].Done
	bufSize := len(s.events)
	if needsDone {
		bufSize++
	}
	ch := make(chan core.Event, bufSize)
	for _, e := range s.events {
		ch <- e
	}
	if needsDone {
		ch <- core.Event{Type: core.EventResult, Done: true}
	}
	close(ch)
	return ch
}

func (s *FakeAgentSession) CurrentSessionID() string {
	return s.sessionID
}

func (s *FakeAgentSession) Alive() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.alive && !s.closed
}

func (s *FakeAgentSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.alive = false
	s.closed = true
	return nil
}

// GetPrompts returns all prompts sent to this session (for verification).
func (s *FakeAgentSession) GetPrompts() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.promptQueue
}

// FakeAgent is a fake implementation of Agent for testing.
type FakeAgent struct {
	name                 string
	sessionID            string
	session              *FakeAgentSession
	preConfiguredSession *FakeAgentSession // session from NewFakeAgentWithSession
	sessions             []core.AgentSessionInfo
	stopped              bool
}

func NewFakeAgent(name string) *FakeAgent {
	return &FakeAgent{
		name:     name,
		sessionID: "fake-session-001",
		sessions: []core.AgentSessionInfo{
			{ID: "fake-session-001", Summary: "Test session"},
		},
	}
}

func (a *FakeAgent) Name() string {
	return a.name
}

func (a *FakeAgent) StartSession(ctx context.Context, sessionID string) (core.AgentSession, error) {
	a.sessionID = sessionID
	// Return pre-configured session on first call (from NewFakeAgentWithSession)
	// then create fresh sessions for subsequent calls
	if a.preConfiguredSession != nil {
		a.session = a.preConfiguredSession
		a.preConfiguredSession = nil
		return a.session, nil
	}
	a.session = NewFakeAgentSession(sessionID)
	return a.session, nil
}

func (a *FakeAgent) ListSessions(ctx context.Context) ([]core.AgentSessionInfo, error) {
	return a.sessions, nil
}

func (a *FakeAgent) Stop() error {
	a.stopped = true
	if a.session != nil {
		a.session.Close()
	}
	return nil
}

// GetSession returns the current fake session.
func (a *FakeAgent) GetSession() *FakeAgentSession {
	return a.session
}

// NewFakeAgentWithSession creates a fake agent with a pre-configured session.
// The pre-configured session is returned on the first StartSession call.
// Subsequent StartSession calls create fresh sessions (simulating real agent behavior).
func NewFakeAgentWithSession(name, sessionID string, session *FakeAgentSession) *FakeAgent {
	return &FakeAgent{
		name:                 name,
		preConfiguredSession: session,
		sessionID:            sessionID,
		sessions: []core.AgentSessionInfo{
			{ID: sessionID, Summary: "Test session"},
		},
	}
}
