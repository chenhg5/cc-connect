package core

import (
	"context"
	"testing"
)

// validatingAgent wraps a controllableAgent and adds an opt-in
// SessionIDValidator so we can pin the engine's behavior for issue #599:
// when the stored session ID is rejected by the agent, the engine must
// stop sending instead of resuming the wrong one or replacing the selection.
type validatingAgent struct {
	controllableAgent
	validateFunc func(ctx context.Context, sessionID string) bool
}

func (a *validatingAgent) ValidateSessionID(ctx context.Context, sessionID string) bool {
	if a.validateFunc == nil {
		return true // default: trust whatever ID the Session has
	}
	return a.validateFunc(ctx, sessionID)
}

var _ SessionIDValidator = (*validatingAgent)(nil)

// Rejecting a session still prevents cross-project leakage, but must not
// discard the selected context by silently starting a different conversation.
func TestIssue599_InvalidSessionIDBlocksSendAndRetainsSelection(t *testing.T) {
	calls := 0
	agent := &validatingAgent{validateFunc: func(context.Context, string) bool { return false }}
	agent.startSessionFn = func(context.Context, string) (AgentSession, error) {
		calls++
		return newControllableSession("fresh-id"), nil
	}
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	defer e.cancel()
	s := &Session{AgentSessionID: "leaked-id-from-other-project"}
	state := e.getOrCreateInteractiveStateWith("test:user1", p, "ctx", s, e.sessions, nil, "")
	if calls != 0 || state.agentSession != nil || state.startupError == "" {
		t.Fatalf("invalid session must block startup: calls=%d, error=%q", calls, state.startupError)
	}
	if s.GetAgentSessionID() != "leaked-id-from-other-project" {
		t.Fatal("validation failure discarded the selected session")
	}
}

// TestIssue599_ValidSessionIDPreserved is the negative case: when the
// agent says the ID is valid, the engine must pass it through to
// StartSession so the resume actually resumes.
func TestIssue599_ValidSessionIDPreserved(t *testing.T) {
	var startedWith string
	sess := newControllableSession("resumed-id")
	agent := &validatingAgent{
		controllableAgent: controllableAgent{nextSession: sess},
		validateFunc: func(_ context.Context, _ string) bool {
			return true
		},
	}
	agent.startSessionFn = func(_ context.Context, sessionID string) (AgentSession, error) {
		startedWith = sessionID
		return sess, nil
	}

	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	key := "test:user1"

	s := &Session{AgentSessionID: "valid-id-abc"}

	e.getOrCreateInteractiveStateWith(key, p, "ctx", s, e.sessions, nil, "")

	if startedWith != "valid-id-abc" {
		t.Errorf("StartSession called with %q, want %q (resume path)", startedWith, "valid-id-abc")
	}
}

// TestIssue599_AgentWithoutValidatorNotBlocked ensures the validation
// gate is opt-in: agents that do not implement SessionIDValidator
// continue to work as before (the existing assumption is that engine
// callers only persist valid IDs).
func TestIssue599_AgentWithoutValidatorNotBlocked(t *testing.T) {
	var startedWith string
	sess := newControllableSession("resumed-id")
	agent := &controllableAgent{nextSession: sess}
	agent.startSessionFn = func(_ context.Context, sessionID string) (AgentSession, error) {
		startedWith = sessionID
		return sess, nil
	}

	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	key := "test:user1"

	s := &Session{AgentSessionID: "any-id"}

	e.getOrCreateInteractiveStateWith(key, p, "ctx", s, e.sessions, nil, "")

	if startedWith != "any-id" {
		t.Errorf("StartSession called with %q, want %q (no validator = pass through)", startedWith, "any-id")
	}
}
