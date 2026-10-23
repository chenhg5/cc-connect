package core

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// startUnsolicitedQuestionTest starts an unsolicited reader on a session that
// records permission responses, as the reader runs after a turn has ended.
func startUnsolicitedQuestionTest(t *testing.T, approveAll bool) (*Engine, *stubPlatformEngine, *permRecordingSession, *interactiveState) {
	t.Helper()
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() {
		if err := e.Stop(); err != nil {
			t.Errorf("stop engine: %v", err)
		}
	})

	agentSession := &permRecordingSession{
		controllableAgentSession: controllableAgentSession{
			sessionID: "unsol-askq",
			alive:     true,
			events:    make(chan Event, 8),
			closed:    make(chan struct{}),
		},
	}
	const key = "test:askq:u1"
	session := e.sessions.GetOrCreateActive(key)
	state := &interactiveState{
		agentSession: agentSession,
		platform:     p,
		replyCtx:     "ctx",
		approveAll:   approveAll,
	}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()

	e.startUnsolicitedReader(state, session, e.sessions, key, "")
	t.Cleanup(func() { e.stopUnsolicitedReader(state) })
	return e, p, agentSession, state
}

func backgroundQuestion() Event {
	return Event{
		Type:         EventPermissionRequest,
		RequestID:    "req-askq",
		ToolName:     "AskUserQuestion",
		ToolInputRaw: map[string]any{"questions": []any{}},
		Questions: []UserQuestion{{
			Question: "Which branch should I use?",
			Options:  []UserQuestionOption{{Label: "master"}, {Label: "release"}},
		}},
	}
}

func waitForPending(t *testing.T, state *interactiveState) *pendingPermission {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		state.mu.Lock()
		pending := state.pending
		state.mu.Unlock()
		if pending != nil {
			return pending
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for the question to be registered")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func waitForPermResponse(t *testing.T, s *permRecordingSession) PermissionResult {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		s.mu.Lock()
		calls, result := s.permCalls, s.lastPermResult
		s.mu.Unlock()
		if calls > 0 {
			return result
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for a permission response")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// A background subagent's AskUserQuestion must reach the user and carry their
// answer back, rather than being auto-denied as a tool permission.
func TestUnsolicitedReader_AsksQuestionAndRelaysAnswer(t *testing.T) {
	for _, approveAll := range []bool{false, true} {
		e, p, agentSession, state := startUnsolicitedQuestionTest(t, approveAll)
		agentSession.events <- backgroundQuestion()

		waitForPending(t, state)
		sent := waitForPlatformSend(p, 1, 5*time.Second)
		if len(sent) == 0 || !strings.Contains(sent[0], "Which branch should I use?") {
			t.Fatalf("approveAll=%v: expected the question to be shown, got %q", approveAll, sent)
		}
		for _, s := range sent {
			if strings.Contains(s, "auto-denied") {
				t.Fatalf("approveAll=%v: question was auto-denied: %q", approveAll, s)
			}
		}
		agentSession.mu.Lock()
		calls := agentSession.permCalls
		agentSession.mu.Unlock()
		if calls != 0 {
			t.Fatalf("approveAll=%v: responded before the user answered", approveAll)
		}

		if !e.handlePendingPermission(p, &Message{SessionKey: "test:askq:u1", ReplyCtx: "ctx"}, "2", "test:askq:u1") {
			t.Fatalf("approveAll=%v: answer was not taken as the question's answer", approveAll)
		}
		result := waitForPermResponse(t, agentSession)
		if result.Behavior != "allow" {
			t.Fatalf("approveAll=%v: expected allow, got %q", approveAll, result.Behavior)
		}
		answers, _ := result.UpdatedInput["answers"].(map[string]any)
		if answers["Which branch should I use?"] != "release" {
			t.Fatalf("approveAll=%v: expected answer release, got %v", approveAll, result.UpdatedInput)
		}
	}
}

// When a new turn takes over the session before the user answers, the
// question is withdrawn so the agent is not left waiting on it.
func TestUnsolicitedReader_WithdrawsUnansweredQuestionOnCancel(t *testing.T) {
	e, _, agentSession, state := startUnsolicitedQuestionTest(t, false)
	agentSession.events <- backgroundQuestion()
	pending := waitForPending(t, state)

	e.stopUnsolicitedReader(state)

	result := waitForPermResponse(t, agentSession)
	if result.Behavior != "deny" {
		t.Fatalf("expected the withdrawn question to be denied, got %q", result.Behavior)
	}
	state.mu.Lock()
	left := state.pending
	state.mu.Unlock()
	if left != nil {
		t.Fatal("withdrawn question still pending")
	}
	select {
	case <-pending.Resolved:
	default:
		t.Fatal("withdrawn question not resolved")
	}
}

// Answering and withdrawing the same question race; exactly one of them may
// respond to the agent, and the outcome is written by that side only.
func TestUnsolicitedReader_AnswerAndWithdrawRespondOnce(t *testing.T) {
	for i := 0; i < 30; i++ {
		e, p, agentSession, state := startUnsolicitedQuestionTest(t, false)
		agentSession.events <- backgroundQuestion()
		waitForPending(t, state)

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			e.handlePendingPermission(p, &Message{SessionKey: "test:askq:u1", ReplyCtx: "ctx"}, "2", "test:askq:u1")
		}()
		go func() {
			defer wg.Done()
			e.stopUnsolicitedReader(state)
		}()
		wg.Wait()

		waitForPermResponse(t, agentSession)
		time.Sleep(20 * time.Millisecond) // let a second, wrong response land
		agentSession.mu.Lock()
		calls := agentSession.permCalls
		agentSession.mu.Unlock()
		if calls != 1 {
			t.Fatalf("round %d: expected exactly one response to the question, got %d", i, calls)
		}
	}
}

// A message that cannot answer a background question (attachments only) must
// not start a turn that would take the woken turn's events; the user is asked
// to answer first and the question stays pending.
func TestUnsolicitedReader_AttachmentWaitsForAnswer(t *testing.T) {
	e, p, agentSession, state := startUnsolicitedQuestionTest(t, false)
	agentSession.events <- backgroundQuestion()
	pending := waitForPending(t, state)
	waitForPlatformSend(p, 1, 5*time.Second)

	msg := &Message{SessionKey: "test:askq:u1", ReplyCtx: "ctx", Images: []ImageAttachment{{MimeType: "image/png", Data: []byte("png")}}}
	if !e.handlePendingPermission(p, msg, "", "test:askq:u1") {
		t.Fatal("an attachment-only message started a turn while a background question waits")
	}
	sent := p.getSent()
	if !strings.Contains(strings.Join(sent, "\n"), "answer the question first") {
		t.Fatalf("expected an answer-first notice, got %q", sent)
	}
	state.mu.Lock()
	still := state.pending
	state.mu.Unlock()
	agentSession.mu.Lock()
	calls := agentSession.permCalls
	agentSession.mu.Unlock()
	if still != pending || calls != 0 {
		t.Fatalf("question should still wait unanswered: pending=%v responses=%d", still == pending, calls)
	}
}

// A foreground turn's question keeps its behaviour: an attachment-only message
// is left to the busy-session queue.
func TestForegroundQuestion_AttachmentNotHeld(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() {
		if err := e.Stop(); err != nil {
			t.Errorf("stop engine: %v", err)
		}
	})
	pending := &pendingPermission{RequestID: "fg", ToolName: "AskUserQuestion", Questions: backgroundQuestion().Questions, Resolved: make(chan struct{})}
	state := &interactiveState{platform: p, replyCtx: "ctx", pending: pending}
	e.interactiveMu.Lock()
	e.interactiveStates["test:askq:u1"] = state
	e.interactiveMu.Unlock()

	msg := &Message{SessionKey: "test:askq:u1", ReplyCtx: "ctx", Images: []ImageAttachment{{MimeType: "image/png", Data: []byte("png")}}}
	if e.handlePendingPermission(p, msg, "", "test:askq:u1") {
		t.Fatal("attachment-only message was held for a foreground question")
	}
}
