package core

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// resumeFailSession simulates an agent session whose --resume target was
// pruned: with failOnSend every Send fails with "No conversation found".
type resumeFailSession struct {
	controllableAgentSession
	failOnSend bool
	sends      int
}

func (s *resumeFailSession) Send(_ string, _ string, _ []ImageAttachment, _ []FileAttachment) error {
	s.sends++
	if s.failOnSend {
		s.events <- Event{Type: EventError, Error: errors.New("No conversation found with session ID: 11111111-2222-3333-4444-555555555555")}
		return nil
	}
	s.events <- Event{Type: EventText, Content: "retried answer"}
	s.events <- Event{Type: EventResult, Content: "retried answer", Done: true}
	return nil
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

	if second.sends != 1 {
		t.Fatalf("expected the prompt to be retried exactly once on a fresh session, got %d sends", second.sends)
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
