package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type failedPermissionAgent struct {
	stubAgent
	mu     sync.Mutex
	first  *failedPermissionSession
	starts int
}

func (a *failedPermissionAgent) StartSession(context.Context, string) (AgentSession, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.starts++
	if a.starts == 1 {
		return a.first, nil
	}
	s := newCUJAgentSession()
	s.reply = "fresh session handled followup"
	return s, nil
}

type failedPermissionSession struct {
	*cujAgentSession
	question bool
	die      bool
	sends    int
}

func (s *failedPermissionSession) Send(string, string, []ImageAttachment, []FileAttachment) error {
	s.mu.Lock()
	s.sends++
	n := s.sends
	s.mu.Unlock()
	if n > 1 {
		s.events <- Event{Type: EventResult, Content: "queued followup handled", Done: true}
		return nil
	}
	ev := Event{Type: EventPermissionRequest, RequestID: "fail-without-retry", ToolName: "Bash", ToolInput: "test command"}
	if s.question {
		ev.ToolName = "AskUserQuestion"
		ev.Questions = []UserQuestion{{Question: "Choose a value", Options: []UserQuestionOption{{Label: "A"}}}}
	}
	s.events <- ev
	return nil
}
func (s *failedPermissionSession) RespondPermission(string, PermissionResult) error {
	if s.die {
		s.closed.Set(true)
	}
	return errors.New("transport failed without retry")
}

// Three user actions: start work, answer once (receipt fails), then send
// unrelated work. Neither ordinary approvals nor question retries may swallow
// the third message, and the foreground reader must still consume completion.
func TestCUJ_C8_FailedPermissionWithoutRetryQueuesNextMessage(t *testing.T) {
	for _, question := range []bool{false, true} {
		t.Run(fmt.Sprint(question), func(t *testing.T) {
			env := newCUJEnv(t)
			s := &failedPermissionSession{cujAgentSession: newCUJAgentSession(), question: question}
			env.engine.agent = &failedPermissionAgent{first: s}
			t.Cleanup(func() { _ = env.engine.Stop() })
			env.userSends("user", "start work")
			prompt, answer := "Bash", "allow"
			if question {
				prompt, answer = "Choose a value", "A"
			}
			env.waitFor("permission", time.Second, func() bool { return env.sentContains(prompt) })
			env.userSends("user", answer)
			env.waitFor("failed receipt", time.Second, func() bool { return env.sentContains("transport failed without retry") })
			env.userSends("user", "unrelated followup")
			env.waitFor("queued receipt", time.Second, func() bool { return env.sentContains(env.engine.i18n.T(MsgMessageQueued)) })
			s.events <- Event{Type: EventResult, Content: "original turn completed", Done: true}
			env.waitFor("followup processed", 2*time.Second, func() bool { return env.sentContains("queued followup handled") })
		})
	}
}

// A failed receipt must restart the event-idle timer even if nobody retries
// and the agent produces no further events. The next user can start fresh.
func TestCUJ_C9_FailedPermissionWithoutRetryIdleCloses(t *testing.T) {
	env := newCUJEnv(t)
	s := &failedPermissionSession{cujAgentSession: newCUJAgentSession()}
	env.engine.agent = &failedPermissionAgent{first: s}
	env.engine.eventIdleTimeout = 150 * time.Millisecond
	t.Cleanup(func() { _ = env.engine.Stop() })
	key := env.userSends("user", "start work")
	env.waitFor("permission", time.Second, func() bool { return env.sentContains("Bash") })
	env.userSends("user", "allow")
	env.waitFor("idle timeout after failure", 2*time.Second, func() bool { return env.sentContains("timed out") && !env.activeSession(key).Busy() })
	env.userSends("user", "new work after timeout")
	env.waitFor("fresh reply", 2*time.Second, func() bool { return env.sentContains("fresh session handled followup") })
}

func TestCUJ_C10_FailedPermissionDeadAgentAbortsTurn(t *testing.T) {
	env := newCUJEnv(t)
	s := &failedPermissionSession{cujAgentSession: newCUJAgentSession(), die: true}
	env.engine.agent = &failedPermissionAgent{first: s}
	t.Cleanup(func() { _ = env.engine.Stop() })
	key := env.userSends("user", "start work")
	env.waitFor("permission", time.Second, func() bool { return env.sentContains("Bash") })
	env.userSends("user", "allow")
	env.waitFor("dead turn released", time.Second, func() bool {
		return env.sentContains("transport failed without retry") && !env.activeSession(key).Busy()
	})
	env.userSends("user", "new work after exit")
	env.waitFor("fresh reply", 2*time.Second, func() bool { return env.sentContains("fresh session handled followup") })
}

type blockedPermissionSession struct {
	stubAgentSession
	entered chan struct{}
	release chan struct{}
}

func (s *blockedPermissionSession) RespondPermission(string, PermissionResult) error {
	close(s.entered)
	<-s.release
	return nil
}

func TestPermissionResponse_SlowAdapterDoesNotBlockSecondCallback(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	s := &blockedPermissionSession{entered: make(chan struct{}), release: make(chan struct{})}
	pending := &pendingPermission{RequestID: "slow", Resolved: make(chan struct{})}
	state := &interactiveState{agentSession: s, pending: pending}
	e.interactiveStates["test:user"] = state
	msg := &Message{SessionKey: "test:user", Content: "allow", ReplyCtx: "ctx"}
	first := make(chan struct{})
	go func() { e.handlePendingPermission(p, msg, "allow", ""); close(first) }()
	select {
	case <-s.entered:
	case <-time.After(time.Second):
		t.Fatal("first response not started")
	}
	second := make(chan struct{})
	go func() { e.handlePendingPermission(p, msg, "allow", ""); close(second) }()
	select {
	case <-second:
	case <-time.After(time.Second):
		close(s.release)
		t.Fatal("second callback blocked behind adapter")
	}
	if got := strings.Join(p.getSent(), " "); !strings.Contains(got, e.i18n.T(MsgPermissionSubmitting)) {
		t.Fatalf("missing submitting acknowledgement: %s", got)
	}
	close(s.release)
	<-first
}

func TestPermissionResponse_WithdrawnQuestionAcknowledged(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	pending := &pendingPermission{RequestID: "stale", Questions: []UserQuestion{{Question: "pick"}}, Resolved: make(chan struct{})}
	state := &interactiveState{pending: pending, stopped: true}
	e.interactiveStates["test:user"] = state
	e.handlePendingPermission(p, &Message{SessionKey: "test:user", ReplyCtx: "ctx"}, "answer", "")
	if got := strings.Join(p.getSent(), " "); !strings.Contains(got, e.i18n.T(MsgPermissionStale)) {
		t.Fatalf("missing stale acknowledgement: %s", got)
	}
}
