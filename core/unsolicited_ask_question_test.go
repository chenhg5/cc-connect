package core

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// questionSession records every permission response by request id.
type questionSession struct {
	controllableAgentSession
	mu        sync.Mutex
	responses []questionResponse
}

type questionResponse struct {
	id     string
	result PermissionResult
}

func (s *questionSession) RespondPermission(id string, res PermissionResult) error {
	s.mu.Lock()
	s.responses = append(s.responses, questionResponse{id, res})
	s.mu.Unlock()
	return nil
}

func (s *questionSession) recorded() []questionResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]questionResponse(nil), s.responses...)
}

// waitForResponses waits until n responses are recorded and returns them.
func (s *questionSession) waitForResponses(t *testing.T, n int) []questionResponse {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if got := s.recorded(); len(got) >= n {
			return got
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %d permission responses, got %v", n, s.recorded())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

const questionKey = "test:askq:u1"

// startUnsolicitedQuestionTest starts an unsolicited reader, as runs after a
// turn has ended, on a session that records permission responses.
func startUnsolicitedQuestionTest(t *testing.T, approveAll bool) (*Engine, *stubPlatformEngine, *questionSession, *interactiveState) {
	t.Helper()
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() {
		if err := e.Stop(); err != nil {
			t.Errorf("stop engine: %v", err)
		}
	})

	agentSession := &questionSession{
		controllableAgentSession: controllableAgentSession{
			sessionID: "unsol-askq",
			alive:     true,
			events:    make(chan Event, 8),
			closed:    make(chan struct{}),
		},
	}
	session := e.sessions.GetOrCreateActive(questionKey)
	state := &interactiveState{
		agentSession: agentSession,
		platform:     p,
		replyCtx:     "ctx",
		approveAll:   approveAll,
	}
	e.interactiveMu.Lock()
	e.interactiveStates[questionKey] = state
	e.interactiveMu.Unlock()

	e.startUnsolicitedReader(state, session, e.sessions, questionKey, "")
	t.Cleanup(func() { e.stopUnsolicitedReader(state) })
	return e, p, agentSession, state
}

func backgroundQuestion(id, question string) Event {
	return Event{
		Type:         EventPermissionRequest,
		RequestID:    id,
		ToolName:     "AskUserQuestion",
		ToolInputRaw: map[string]any{"questions": []any{}},
		Questions: []UserQuestion{{
			Question: question,
			Options:  []UserQuestionOption{{Label: "master"}, {Label: "release"}},
		}},
	}
}

// waitForPending waits until the request with id is the pending one.
func waitForPending(t *testing.T, state *interactiveState, id string) *pendingPermission {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		state.mu.Lock()
		pending := state.pending
		state.mu.Unlock()
		if pending != nil && pending.RequestID == id {
			return pending
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for question %s to be asked", id)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// waitForQuestionReader sends a result behind the queued questions. Seeing it
// on the platform proves the reader has processed every preceding event, even
// on a loaded runner; inspecting the reader-owned queue would race.
func waitForQuestionReader(t *testing.T, p *stubPlatformEngine, session *questionSession) {
	t.Helper()
	const marker = "question-reader-barrier"
	session.events <- Event{Type: EventResult, Content: marker, Done: true}
	deadline := time.After(5 * time.Second)
	for !strings.Contains(strings.Join(p.getSent(), "\n"), marker) {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for the question reader to process queued events")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func answer(e *Engine, p *stubPlatformEngine, content string) bool {
	return e.handlePendingPermission(p, &Message{SessionKey: questionKey, ReplyCtx: "ctx"}, content, questionKey)
}

func answerOf(t *testing.T, r questionResponse, question string) string {
	t.Helper()
	if r.result.Behavior != "allow" {
		t.Fatalf("%s: expected allow, got %q", r.id, r.result.Behavior)
	}
	answers, _ := r.result.UpdatedInput["answers"].(map[string]any)
	got, _ := answers[question].(string)
	return got
}

// A background AskUserQuestion must reach the user and carry their answer
// back, rather than being auto-denied as a tool permission.
func TestUnsolicitedReader_AsksQuestionAndRelaysAnswer(t *testing.T) {
	for _, approveAll := range []bool{false, true} {
		e, p, agentSession, state := startUnsolicitedQuestionTest(t, approveAll)
		agentSession.events <- backgroundQuestion("q1", "Which branch should I use?")

		waitForPending(t, state, "q1")
		sent := waitForPlatformSend(p, 1, 5*time.Second)
		if len(sent) == 0 || !strings.Contains(sent[0], "Which branch should I use?") {
			t.Fatalf("approveAll=%v: expected the question to be shown, got %q", approveAll, sent)
		}
		for _, s := range sent {
			if strings.Contains(s, "auto-denied") {
				t.Fatalf("approveAll=%v: question was auto-denied: %q", approveAll, s)
			}
		}
		if got := agentSession.recorded(); len(got) != 0 {
			t.Fatalf("approveAll=%v: responded before the user answered: %v", approveAll, got)
		}

		if !answer(e, p, "2") {
			t.Fatalf("approveAll=%v: answer was not taken as the question's answer", approveAll)
		}
		r := agentSession.waitForResponses(t, 1)[0]
		if got := answerOf(t, r, "Which branch should I use?"); got != "release" {
			t.Fatalf("approveAll=%v: expected answer release, got %q", approveAll, got)
		}
	}
}

// A pending question must not stop the reader: other events keep flowing,
// and a tool permission is still denied promptly.
func TestUnsolicitedReader_KeepsReadingWhileQuestionWaits(t *testing.T) {
	e, p, agentSession, state := startUnsolicitedQuestionTest(t, false)
	agentSession.events <- backgroundQuestion("q1", "Which branch should I use?")
	waitForPending(t, state, "q1")

	agentSession.events <- Event{Type: EventPermissionRequest, RequestID: "bash-1", ToolName: "Bash"}
	r := agentSession.waitForResponses(t, 1)[0]
	if r.id != "bash-1" || r.result.Behavior != "deny" {
		t.Fatalf("expected the tool permission to be denied while the question waits, got %v", r)
	}

	agentSession.events <- Event{Type: EventResult, Content: "subagent report", Done: true}
	deadline := time.After(5 * time.Second)
	for !strings.Contains(strings.Join(p.getSent(), "\n"), "subagent report") {
		select {
		case <-deadline:
			t.Fatalf("result not relayed while the question waits: %q", p.getSent())
		case <-time.After(10 * time.Millisecond):
		}
	}

	state.mu.Lock()
	pending := state.pending
	state.mu.Unlock()
	if pending == nil || pending.RequestID != "q1" {
		t.Fatal("question no longer pending")
	}
	if !answer(e, p, "1") {
		t.Fatal("answer not taken")
	}
	r = agentSession.waitForResponses(t, 2)[1]
	if r.id != "q1" || answerOf(t, r, "Which branch should I use?") != "master" {
		t.Fatalf("unexpected answer %v", r)
	}
}

// A second question waits for the first instead of replacing it, then is
// shown and answered in turn.
func TestUnsolicitedReader_AsksQuestionsOneAtATime(t *testing.T) {
	e, p, agentSession, state := startUnsolicitedQuestionTest(t, false)
	agentSession.events <- backgroundQuestion("q1", "First?")
	agentSession.events <- backgroundQuestion("q2", "Second?")
	waitForPending(t, state, "q1")

	waitForQuestionReader(t, p, agentSession)
	if strings.Contains(strings.Join(p.getSent(), "\n"), "Second?") {
		t.Fatal("second question shown while the first waits")
	}
	answer(e, p, "1")
	waitForPending(t, state, "q2")
	answer(e, p, "2")

	got := agentSession.waitForResponses(t, 2)
	if got[0].id != "q1" || answerOf(t, got[0], "First?") != "master" ||
		got[1].id != "q2" || answerOf(t, got[1], "Second?") != "release" {
		t.Fatalf("unexpected answers %v", got)
	}
}

// When the reader exits before the user answers, the shown and the queued
// questions are withdrawn so the agent is not left waiting on them.
func TestUnsolicitedReader_WithdrawsUnansweredQuestionsOnExit(t *testing.T) {
	e, p, agentSession, state := startUnsolicitedQuestionTest(t, false)
	agentSession.events <- backgroundQuestion("q1", "First?")
	agentSession.events <- backgroundQuestion("q2", "Second?")
	pending := waitForPending(t, state, "q1")
	waitForQuestionReader(t, p, agentSession)

	e.stopUnsolicitedReader(state)

	got := agentSession.waitForResponses(t, 2)
	for i, id := range []string{"q1", "q2"} {
		if got[i].id != id || got[i].result.Behavior != "deny" {
			t.Fatalf("expected %s denied, got %v", id, got)
		}
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
// respond to the agent.
func TestUnsolicitedReader_AnswerAndWithdrawRespondOnce(t *testing.T) {
	for i := 0; i < 30; i++ {
		e, p, agentSession, state := startUnsolicitedQuestionTest(t, false)
		agentSession.events <- backgroundQuestion("q1", "Which branch should I use?")
		waitForPending(t, state, "q1")

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			answer(e, p, "2")
		}()
		go func() {
			defer wg.Done()
			e.stopUnsolicitedReader(state)
		}()
		wg.Wait()

		agentSession.waitForResponses(t, 1)
		time.Sleep(20 * time.Millisecond) // let a second, wrong response land
		if got := agentSession.recorded(); len(got) != 1 {
			t.Fatalf("round %d: expected exactly one response to the question, got %v", i, got)
		}
	}
}

// A message that cannot answer a background question (attachments only) must
// not start a turn that would take the woken turn's events; the user is asked
// to answer first and the question stays pending.
func TestUnsolicitedReader_AttachmentWaitsForAnswer(t *testing.T) {
	e, p, agentSession, state := startUnsolicitedQuestionTest(t, false)
	agentSession.events <- backgroundQuestion("q1", "Which branch should I use?")
	pending := waitForPending(t, state, "q1")
	waitForPlatformSend(p, 1, 5*time.Second)

	msg := &Message{SessionKey: questionKey, ReplyCtx: "ctx", Images: []ImageAttachment{{MimeType: "image/png", Data: []byte("png")}}}
	if !e.handlePendingPermission(p, msg, "", questionKey) {
		t.Fatal("an attachment-only message started a turn while a background question waits")
	}
	if sent := p.getSent(); !strings.Contains(strings.Join(sent, "\n"), "answer the question first") {
		t.Fatalf("expected an answer-first notice, got %q", sent)
	}
	state.mu.Lock()
	still := state.pending
	state.mu.Unlock()
	if still != pending || len(agentSession.recorded()) != 0 {
		t.Fatalf("question should still wait unanswered: pending=%v responses=%v", still == pending, agentSession.recorded())
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
	question := backgroundQuestion("fg", "Which branch should I use?")
	pending := &pendingPermission{RequestID: "fg", ToolName: "AskUserQuestion", Questions: question.Questions, Resolved: make(chan struct{})}
	state := &interactiveState{platform: p, replyCtx: "ctx", pending: pending}
	e.interactiveMu.Lock()
	e.interactiveStates[questionKey] = state
	e.interactiveMu.Unlock()

	msg := &Message{SessionKey: questionKey, ReplyCtx: "ctx", Images: []ImageAttachment{{MimeType: "image/png", Data: []byte("png")}}}
	if e.handlePendingPermission(p, msg, "", questionKey) {
		t.Fatal("attachment-only message was held for a foreground question")
	}
}
