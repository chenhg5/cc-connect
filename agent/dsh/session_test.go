package dsh

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// drainEvents reads events until a Done event arrives or timeout expires.
func drainEvents(t *testing.T, s *dshSession, timeout time.Duration) []core.Event {
	t.Helper()
	var out []core.Event
	deadline := time.After(timeout)
	for {
		select {
		case evt, ok := <-s.events:
			if !ok {
				return out
			}
			out = append(out, evt)
			if evt.Type == core.EventResult && evt.Done {
				return out
			}
		case <-deadline:
			t.Fatalf("timed out waiting for turn end; got %d events", len(out))
			return out
		}
	}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		if cond() {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for: %s", msg)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func newTestAgent(t *testing.T, m *mockDshServer, workDir string) *Agent {
	t.Helper()
	ts := startMockServer(m)
	t.Cleanup(func() { ts.Close() })
	t.Cleanup(m.close)

	agent, err := New(map[string]any{"base_url": ts.URL, "work_dir": workDir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return agent.(*Agent)
}

// TestStartSession_CreateAndResume verifies fresh creation vs resume reuse.
func TestStartSession_CreateAndResume(t *testing.T) {
	m := newMockDshServer()
	agent := newTestAgent(t, m, "/tmp/proj")

	ctx := context.Background()
	s1, err := agent.StartSession(ctx, "")
	if err != nil {
		t.Fatalf("StartSession fresh: %v", err)
	}
	if s1.CurrentSessionID() == "" {
		t.Fatal("expected a session id after fresh creation")
	}
	if !s1.Alive() {
		t.Fatal("expected session alive after start")
	}
	if m.createdCount() != 1 {
		t.Fatalf("expected 1 created session, got %d", m.createdCount())
	}

	// Resume with the same id must reuse it (server-side idempotency).
	id := s1.CurrentSessionID()
	s2, err := agent.StartSession(ctx, id)
	if err != nil {
		t.Fatalf("StartSession resume: %v", err)
	}
	if s2.CurrentSessionID() != id {
		t.Fatalf("resume id mismatch: got %q want %q", s2.CurrentSessionID(), id)
	}
	if m.createdCount() != 1 {
		t.Fatalf("resume must not create a new session, got %d created", m.createdCount())
	}

	_ = s1.Close()
	_ = s2.Close()
}

// TestStartSession_CreationFailure verifies a clear error when creation fails.
func TestStartSession_CreationFailure(t *testing.T) {
	m := newMockDshServer()
	agent := newTestAgent(t, m, "/tmp/proj")
	m.mu.Lock()
	m.failNext["session.create"] = 1
	m.mu.Unlock()

	_, err := agent.StartSession(context.Background(), "")
	if err == nil {
		t.Fatal("expected error when session.create fails")
	}
	if !strings.Contains(err.Error(), "session.create") {
		t.Fatalf("error should mention session.create, got: %v", err)
	}
}

// TestSession_StreamingTurn verifies the full event mapping of one turn:
// reasoning → text deltas → tool call → tool result → turn end with usage.
func TestSession_StreamingTurn(t *testing.T) {
	m := newMockDshServer()
	agent := newTestAgent(t, m, "/tmp/proj")

	s, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer s.Close()
	ds := s.(*dshSession)
	id := ds.sessionID

	if err := ds.Send("hello", "msg-1", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Emit the turn from the server side.
	m.broadcast("rpc-1", frameEvent(id, evTurnStart(1)))
	m.broadcast("rpc-2", frameEvent(id, evReasoningDelta(1, 1, "thinking...")))
	m.broadcast("rpc-3", frameEvent(id, evTextDelta(1, 1, "Hel")))
	m.broadcast("rpc-4", frameEvent(id, evTextDelta(1, 1, "lo")))
	m.broadcast("rpc-5", frameEvent(id, evToolCall(1, 2, "call-1", "bash", `{"command":"ls"}`)))
	m.broadcast("rpc-6", frameEvent(id, evToolResult(1, 2, "call-1", false, "file1\nfile2")))
	m.broadcast("rpc-7", frameEvent(id, evTextDelta(1, 3, "done!")))
	m.broadcast("rpc-8", frameEvent(id, evTurnEnd(1, "completed")))

	events := drainEvents(t, ds, 5*time.Second)

	var text strings.Builder
	var sawThinking, sawToolUse, sawToolResult bool
	var final *core.Event
	for i := range events {
		e := events[i]
		switch e.Type {
		case core.EventText:
			text.WriteString(e.Content)
		case core.EventThinking:
			sawThinking = true
			if e.Content != "thinking..." {
				t.Fatalf("unexpected thinking content: %q", e.Content)
			}
		case core.EventToolUse:
			sawToolUse = true
			if e.ToolName != "bash" || !strings.Contains(e.ToolInput, "ls") {
				t.Fatalf("unexpected tool use: %+v", e)
			}
		case core.EventToolResult:
			sawToolResult = true
			if e.ToolName != "bash" || e.ToolStatus != "completed" {
				t.Fatalf("unexpected tool result: %+v", e)
			}
		case core.EventResult:
			final = &e
		}
	}
	if text.String() != "Hellodone!" {
		t.Fatalf("streamed text mismatch: %q", text.String())
	}
	if !sawThinking || !sawToolUse || !sawToolResult {
		t.Fatalf("missing event kinds: thinking=%v toolUse=%v toolResult=%v", sawThinking, sawToolUse, sawToolResult)
	}
	if final == nil || !final.Done || final.SessionID != id {
		t.Fatalf("expected done result with session id, got %+v", final)
	}
}

// TestSession_ToolCallDedupe verifies a tool call surfaced once even though
// both the streaming delta and the durable event carry it.
func TestSession_ToolCallDedupe(t *testing.T) {
	m := newMockDshServer()
	agent := newTestAgent(t, m, "/tmp/proj")

	s, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer s.Close()
	ds := s.(*dshSession)
	id := ds.sessionID

	if err := ds.Send("do it", "msg-1", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	m.broadcast("rpc-1", frameEvent(id, evTurnStart(1)))
	// Streaming delta first, then the durable tool/call for the same id.
	m.broadcast("rpc-2", frameEvent(id, ev("assistant/chunk", map[string]any{
		"turn": 1, "step": 2,
		"chunk": map[string]any{
			"type": "tool-call-delta", "index": 0, "id": "call-x",
			"name": "bash", "argumentsDelta": `{"command":"ls"}`,
		},
	})))
	m.broadcast("rpc-3", frameEvent(id, evToolCall(1, 2, "call-x", "bash", `{"command":"ls"}`)))
	m.broadcast("rpc-4", frameEvent(id, evTurnEnd(1, "completed")))

	events := drainEvents(t, ds, 5*time.Second)
	var toolUses int
	for _, e := range events {
		if e.Type == core.EventToolUse {
			toolUses++
		}
	}
	if toolUses != 1 {
		t.Fatalf("expected exactly 1 EventToolUse, got %d", toolUses)
	}
}

// TestSession_ToolResultFailed verifies failed tool results carry status.
func TestSession_ToolResultFailed(t *testing.T) {
	m := newMockDshServer()
	agent := newTestAgent(t, m, "/tmp/proj")

	s, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer s.Close()
	ds := s.(*dshSession)
	id := ds.sessionID

	if err := ds.Send("go", "msg-1", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	m.broadcast("rpc-1", frameEvent(id, evTurnStart(1)))
	m.broadcast("rpc-2", frameEvent(id, evToolCall(1, 2, "call-e", "bash", `{}`)))
	m.broadcast("rpc-3", frameEvent(id, evToolResult(1, 2, "call-e", true, "permission denied")))
	m.broadcast("rpc-4", frameEvent(id, evTurnEnd(1, "completed")))

	events := drainEvents(t, ds, 5*time.Second)
	for _, e := range events {
		if e.Type == core.EventToolResult {
			if e.ToolStatus != "failed" {
				t.Fatalf("expected failed status, got %q", e.ToolStatus)
			}
			return
		}
	}
	t.Fatal("no EventToolResult received")
}

// TestSession_TurnError verifies error turns surface an error result.
func TestSession_TurnError(t *testing.T) {
	m := newMockDshServer()
	agent := newTestAgent(t, m, "/tmp/proj")

	s, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer s.Close()
	ds := s.(*dshSession)
	id := ds.sessionID

	if err := ds.Send("boom", "msg-1", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	m.broadcast("rpc-1", frameEvent(id, evTurnStart(1)))
	m.broadcast("rpc-2", frameEvent(id, evTurnEnd(1, "error")))

	events := drainEvents(t, ds, 5*time.Second)
	var sawError bool
	for _, e := range events {
		if e.Type == core.EventError {
			sawError = true
		}
		if e.Type == core.EventResult && !e.Done {
			t.Fatal("error turn must still mark Done")
		}
	}
	if !sawError {
		t.Fatal("expected an EventError for an error turn")
	}
}

// TestSession_ApprovalFlow verifies approval/requested frames round-trip
// through RespondPermission to /api/respond.
func TestSession_ApprovalFlow(t *testing.T) {
	m := newMockDshServer()
	agent := newTestAgent(t, m, "/tmp/proj")

	s, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer s.Close()
	ds := s.(*dshSession)
	id := ds.sessionID

	if err := ds.Send("run it", "msg-1", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	m.broadcast("rpc-1", frameEvent(id, evTurnStart(1)))
	m.broadcast("rpc-approve", map[string]any{
		"type":       "approval/requested",
		"sessionId":  id,
		"approvalId": "approval-1",
		"toolName":   "bash",
		"reason":     "run: ls",
	})

	// The engine sees a permission request and responds.
	var perm *core.Event
	deadline := time.After(5 * time.Second)
	for perm == nil {
		select {
		case e := <-ds.events:
			if e.Type == core.EventPermissionRequest {
				perm = &e
			}
		case <-deadline:
			t.Fatal("timed out waiting for permission request")
		}
	}
	if perm.RequestID != "rpc-approve" || perm.ToolName != "bash" {
		t.Fatalf("unexpected permission event: %+v", perm)
	}

	if err := ds.RespondPermission(perm.RequestID, core.PermissionResult{Behavior: "allow"}); err != nil {
		t.Fatalf("RespondPermission: %v", err)
	}

	waitFor(t, 5*time.Second, func() bool { return m.respondCount() == 1 }, "respond call")
	call := m.respondAt(0)
	if call.rpcID != "rpc-approve" {
		t.Fatalf("respond rpcId mismatch: %q", call.rpcID)
	}
	if call.value["approvalId"] != "approval-1" || call.value["sessionId"] != id {
		t.Fatalf("unexpected approval respond payload: %+v", call.value)
	}
	if call.value["outcome"] != "allowed-once" {
		t.Fatalf("expected allowed-once outcome, got %v", call.value["outcome"])
	}

	// Deny path.
	m.broadcast("rpc-deny", map[string]any{
		"type":       "approval/requested",
		"sessionId":  id,
		"approvalId": "approval-2",
		"toolName":   "bash",
	})
	waitFor(t, 5*time.Second, func() bool { return m.respondCount() >= 1 }, "stream alive")

	// Send a second prompt to keep the session coherent, then deny.
	if err := ds.Send("continue", "msg-2", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	m.broadcast("rpc-deny2", map[string]any{
		"type":       "approval/requested",
		"sessionId":  id,
		"approvalId": "approval-3",
		"toolName":   "bash",
	})
	var perm2 *core.Event
	deadline2 := time.After(5 * time.Second)
	for perm2 == nil {
		select {
		case e := <-ds.events:
			if e.Type == core.EventPermissionRequest {
				perm2 = &e
			}
		case <-deadline2:
			t.Fatal("timed out waiting for second permission request")
		}
	}
	if err := ds.RespondPermission(perm2.RequestID, core.PermissionResult{Behavior: "deny"}); err != nil {
		t.Fatalf("RespondPermission deny: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return len(m.responds) == 2
	}, "second respond call")
	if m.respondAt(1).value["outcome"] != "rejected" {
		t.Fatalf("expected rejected outcome, got %+v", m.responds[1].value)
	}
}

// TestSession_QuestionFlow verifies question/requested round-trips with
// answer mapping back to option labels / custom text.
func TestSession_QuestionFlow(t *testing.T) {
	m := newMockDshServer()
	agent := newTestAgent(t, m, "/tmp/proj")

	s, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer s.Close()
	ds := s.(*dshSession)
	id := ds.sessionID

	if err := ds.Send("decide", "msg-1", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	m.broadcast("rpc-1", frameEvent(id, evTurnStart(1)))
	m.broadcast("rpc-q", map[string]any{
		"type":      "question/requested",
		"sessionId": id,
		"questions": []map[string]any{{
			"id":       "q1",
			"question": "Which database?",
			"options": []map[string]any{
				{"label": "Postgres"},
				{"label": "MySQL"},
			},
		}},
	})

	var qev *core.Event
	deadline := time.After(5 * time.Second)
	for qev == nil {
		select {
		case e := <-ds.events:
			if e.Type == core.EventPermissionRequest && len(e.Questions) > 0 {
				qev = &e
			}
		case <-deadline:
			t.Fatal("timed out waiting for question request")
		}
	}
	if qev.ToolName != "AskUserQuestion" || len(qev.Questions) != 1 {
		t.Fatalf("unexpected question event: %+v", qev)
	}
	if qev.Questions[0].Question != "Which database?" || len(qev.Questions[0].Options) != 2 {
		t.Fatalf("question mapping wrong: %+v", qev.Questions[0])
	}

	// Engine-side answer: answers keyed by question text (buildAskQuestionResponse).
	err = ds.RespondPermission(qev.RequestID, core.PermissionResult{
		Behavior: "allow",
		UpdatedInput: map[string]any{
			"answers": map[string]any{"Which database?": "Postgres"},
		},
	})
	if err != nil {
		t.Fatalf("RespondPermission: %v", err)
	}

	waitFor(t, 5*time.Second, func() bool { return m.respondCount() == 1 }, "respond call")
	call := m.respondAt(0)
	if call.rpcID != "rpc-q" {
		t.Fatalf("respond rpcId mismatch: %q", call.rpcID)
	}
	answer, _ := call.value["answer"].(map[string]any)
	answers, _ := answer["answers"].([]any)
	if len(answers) != 1 {
		t.Fatalf("expected 1 answer entry, got %+v", answers)
	}
	entry := answers[0].(map[string]any)
	if entry["id"] != "q1" {
		t.Fatalf("answer id mismatch: %+v", entry)
	}
	selected, _ := entry["selected"].([]any)
	if len(selected) != 1 || selected[0] != "Postgres" {
		t.Fatalf("expected selected=[Postgres], got %+v", entry)
	}

	// Custom free-text answer → custom field.
	m.broadcast("rpc-q2", map[string]any{
		"type":      "question/requested",
		"sessionId": id,
		"questions": []map[string]any{{
			"id":       "q2",
			"question": "Anything else?",
			"options":  []map[string]any{{"label": "No"}},
		}},
	})
	var qev2 *core.Event
	deadline2 := time.After(5 * time.Second)
	for qev2 == nil {
		select {
		case e := <-ds.events:
			if e.Type == core.EventPermissionRequest && len(e.Questions) > 0 {
				qev2 = &e
			}
		case <-deadline2:
			t.Fatal("timed out waiting for second question")
		}
	}
	err = ds.RespondPermission(qev2.RequestID, core.PermissionResult{
		Behavior: "allow",
		UpdatedInput: map[string]any{
			"answers": map[string]any{"Anything else?": "please add a README"},
		},
	})
	if err != nil {
		t.Fatalf("RespondPermission 2: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool { return m.respondCount() == 2 }, "second respond call")
	call2 := m.respondAt(1)
	answer2, _ := call2.value["answer"].(map[string]any)
	answers2, _ := answer2["answers"].([]any)
	entry2 := answers2[0].(map[string]any)
	if entry2["custom"] != "please add a README" {
		t.Fatalf("expected custom answer, got %+v", entry2)
	}
}

// TestRespondPermission_UnknownRequest verifies a clear error for stale ids.
func TestRespondPermission_UnknownRequest(t *testing.T) {
	m := newMockDshServer()
	agent := newTestAgent(t, m, "/tmp/proj")
	s, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer s.Close()
	ds := s.(*dshSession)

	err = ds.RespondPermission("no-such", core.PermissionResult{Behavior: "allow"})
	if err == nil || !strings.Contains(err.Error(), "no pending request") {
		t.Fatalf("expected 'no pending request' error, got %v", err)
	}
}

// TestCancelTurn verifies /stop maps to session.cancel.
func TestCancelTurn(t *testing.T) {
	m := newMockDshServer()
	agent := newTestAgent(t, m, "/tmp/proj")
	s, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer s.Close()
	ds := s.(*dshSession)

	if err := ds.CancelTurn(); err != nil {
		t.Fatalf("CancelTurn: %v", err)
	}
	if m.cancelCount() != 1 {
		t.Fatalf("expected 1 session.cancel call, got %d", m.cancelCount())
	}
}

// TestSession_TurnTimeout verifies the per-turn watchdog cancels long turns.
func TestSession_TurnTimeout(t *testing.T) {
	m := newMockDshServer()
	agent := newTestAgent(t, m, "/tmp/proj")
	agent.mu.Lock()
	agent.timeout = 200 * time.Millisecond
	agent.mu.Unlock()

	s, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer s.Close()
	ds := s.(*dshSession)
	id := ds.sessionID

	if err := ds.Send("slow", "msg-1", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	m.broadcast("rpc-1", frameEvent(id, evTurnStart(1)))
	// No turn/end — watchdog should fire.

	deadline := time.After(5 * time.Second)
	for {
		select {
		case e := <-ds.events:
			if e.Type == core.EventError && strings.Contains(e.Error.Error(), "timed out") {
				goto timedOut
			}
		case <-deadline:
			t.Fatal("timed out waiting for watchdog error")
		}
	}
timedOut:
	waitFor(t, 5*time.Second, func() bool { return m.cancelCount() == 1 }, "cancel after timeout")
}

// TestSession_EnvBootstrap verifies the env block lands in the first prompt
// of a fresh session and not in later ones.
func TestSession_EnvBootstrap(t *testing.T) {
	m := newMockDshServer()
	agent := newTestAgent(t, m, "/tmp/proj")
	agent.SetSessionEnv([]string{"CC_PROJECT=proj-a", "CC_SESSION_KEY=feishu:1:2"})

	s, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer s.Close()
	ds := s.(*dshSession)

	if err := ds.Send("first", "msg-1", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := ds.Send("second", "msg-2", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool { return m.promptCount() == 2 }, "two prompts")

	if !strings.Contains(m.promptAt(0), "CC_PROJECT=proj-a") || !strings.Contains(m.promptAt(0), "CC_SESSION_KEY=feishu:1:2") {
		t.Fatalf("first prompt should carry env block, got: %q", m.promptAt(0))
	}
	if !strings.Contains(m.promptAt(0), "first") {
		t.Fatalf("first prompt should still carry the user text, got: %q", m.promptAt(0))
	}
	if strings.Contains(m.promptAt(1), "CC_PROJECT") {
		t.Fatalf("env block must not repeat on later prompts, got: %q", m.promptAt(1))
	}
}

// TestListSessions verifies cwd filtering and title summaries.
func TestListSessions(t *testing.T) {
	m := newMockDshServer()
	m.addSession("/tmp/proj")
	m.addSession("/tmp/proj")
	m.addSession("/tmp/other")

	agent := newTestAgent(t, m, "/tmp/proj")
	sessions, err := agent.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions for /tmp/proj, got %d", len(sessions))
	}
	for _, s := range sessions {
		if s.ID == "" {
			t.Fatal("session id must not be empty")
		}
	}
}

// TestValidateSessionID verifies the cross-project session guard.
func TestValidateSessionID(t *testing.T) {
	m := newMockDshServer()
	projID := m.addSession("/tmp/proj")
	m.addSession("/tmp/other")

	agent := newTestAgent(t, m, "/tmp/proj")
	if !agent.ValidateSessionID(context.Background(), projID) {
		t.Fatal("expected proj session id to validate")
	}
	if agent.ValidateSessionID(context.Background(), "session-mock-3") {
		t.Fatal("session from another cwd must not validate")
	}
	if agent.ValidateSessionID(context.Background(), "") {
		t.Fatal("empty id must not validate")
	}
}

// TestSession_Close verifies Close terminates the session and events channel.
func TestSession_Close(t *testing.T) {
	m := newMockDshServer()
	agent := newTestAgent(t, m, "/tmp/proj")
	s, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	ds := s.(*dshSession)

	if err := ds.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if ds.Alive() {
		t.Fatal("session must not be alive after Close")
	}
	// Events channel must be closed after the reader drains.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-ds.events:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("events channel not closed after Close")
		}
	}
}

// TestAgent_ModelSwitcher verifies model override application and listing.
func TestAgent_ModelSwitcher(t *testing.T) {
	m := newMockDshServer()
	agent := newTestAgent(t, m, "/tmp/proj")

	agent.SetModel("mock/mock-2")
	if agent.GetModel() != "mock/mock-2" {
		t.Fatalf("GetModel mismatch: %q", agent.GetModel())
	}

	s, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer s.Close()

	models := agent.AvailableModels(context.Background())
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].Name != "mock/mock-1" {
		t.Fatalf("expected provider-prefixed model name, got %q", models[0].Name)
	}
}

// TestSessionList_FractionalUpdatedAt is a regression test: dsh reports
// updatedAt as a fractional millisecond float (e.g. 1786465628680.5632);
// parsing it as int64 made session.list fail, which broke ValidateSessionID
// and /list (every message was treated as a fresh session).
func TestSessionList_FractionalUpdatedAt(t *testing.T) {
	m := newMockDshServer()
	// Inject a session whose updatedAt is a float with fraction.
	m.mu.Lock()
	m.sessions["session-float"] = &mockSession{id: "session-float", cwd: "/tmp/proj", prompts: 1}
	m.mu.Unlock()

	agent := newTestAgent(t, m, "/tmp/proj")
	if !agent.ValidateSessionID(context.Background(), "session-float") {
		t.Fatal("ValidateSessionID must accept sessions with fractional updatedAt")
	}
	sessions, err := agent.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions with fractional updatedAt: %v", err)
	}
	if len(sessions) == 0 {
		t.Fatal("expected at least one listed session")
	}
}

// TestOpenMux_OtherSessionNoiseDoesNotStallOwnTurn is a regression test for
// the stuck-turn bug: the mux stream aggregates every session on the server.
// A flood of frames from OTHER sessions (e.g. a busy Web GUI) used to fill
// the shared frame channel and drop this session's turn/end, leaving the
// engine stuck after a permission answer (and /stop ineffective because the
// engine was still waiting on the turn).
func TestOpenMux_OtherSessionNoiseDoesNotStallOwnTurn(t *testing.T) {
	m := newMockDshServer()
	agent := newTestAgent(t, m, "/tmp/proj")

	s, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer s.Close()
	ds := s.(*dshSession)
	id := ds.sessionID

	if err := ds.Send("go", "msg-1", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Flood the mux with other-session events (5× the old channel capacity)
	// interleaved with this session's text deltas, WITHOUT consuming events.
	for i := 0; i < 1500; i++ {
		other := "session-other-" + fmt.Sprintf("%d", i%7)
		m.broadcast("rpc-other-"+fmt.Sprintf("%d", i), frameEvent(other, evTextDelta(1, 1, "noise")))
		if i%3 == 0 {
			m.broadcast("rpc-self-"+fmt.Sprintf("%d", i), frameEvent(id, evTextDelta(1, 1, "tok ")))
		}
	}

	// The turn/end frame must survive the overflow and complete the turn.
	m.broadcast("rpc-end", frameEvent(id, evTurnEnd(1, "completed")))

	events := drainEvents(t, ds, 10*time.Second)
	var done bool
	for _, e := range events {
		if e.Type == core.EventResult && e.Done {
			done = true
		}
	}
	if !done {
		t.Fatal("turn/end was lost under other-session flood — engine would stay stuck")
	}
}

// TestAgent_ModeSwitcher verifies /mode support: normalization, immediate
// application via the dsh /permission command, and the mode catalog.
func TestAgent_ModeSwitcher(t *testing.T) {
	m := newMockDshServer()
	agent := newTestAgent(t, m, "/tmp/proj")

	// Normalization of aliases.
	if normalizeMode("bypassPermissions") != "yolo" || normalizeMode("auto") != "yolo" {
		t.Fatal("yolo aliases must normalize to yolo")
	}
	if normalizeMode("plan") != "plan" {
		t.Fatal("plan must normalize to plan")
	}
	if normalizeMode("something-else") != "default" {
		t.Fatal("unknown modes must fall back to default")
	}

	modes := agent.PermissionModes()
	if len(modes) != 3 {
		t.Fatalf("expected 3 permission modes, got %d", len(modes))
	}

	// Start a session so the mode change can be applied immediately.
	s, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer s.Close()

	agent.SetMode("yolo")
	if agent.GetMode() != "yolo" {
		t.Fatalf("GetMode mismatch: %q", agent.GetMode())
	}
	waitFor(t, 5*time.Second, func() bool { return m.commandCount() >= 1 }, "/permission command")
	if line := m.commandAt(0); line != "/permission danger-full-access" {
		t.Fatalf("expected /permission danger-full-access, got %q", line)
	}

	agent.SetMode("plan")
	waitFor(t, 5*time.Second, func() bool { return m.commandCount() >= 2 }, "second /permission command")
	if line := m.commandAt(1); line != "/permission read-only" {
		t.Fatalf("expected /permission read-only, got %q", line)
	}

	agent.SetMode("default")
	waitFor(t, 5*time.Second, func() bool { return m.commandCount() >= 3 }, "third /permission command")
	if line := m.commandAt(2); line != "/permission workspace-write" {
		t.Fatalf("expected /permission workspace-write, got %q", line)
	}
}

// TestAgent_ReasoningEffort verifies /reasoning support: the effort override
// is applied through session.selectModel(reasoningEffort) at session start and
// the available efforts are surfaced from the model catalog.
func TestAgent_ReasoningEffort(t *testing.T) {
	m := newMockDshServer()
	agent := newTestAgent(t, m, "/tmp/proj")

	agent.SetReasoningEffort("high")
	if agent.GetReasoningEffort() != "high" {
		t.Fatalf("GetReasoningEffort mismatch: %q", agent.GetReasoningEffort())
	}

	s, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer s.Close()

	// The effort override must ride the selectModel call.
	waitFor(t, 5*time.Second, func() bool { return m.selectModelCount() >= 1 }, "selectModel call")
	provider, model, effort := m.selectModelAt(0)
	if provider != "mock" || model != "mock-1" || effort != "high" {
		t.Fatalf("selectModel payload mismatch: provider=%q model=%q effort=%q", provider, model, effort)
	}

	// The catalog refreshes the available efforts after a session starts.
	efforts := agent.AvailableReasoningEfforts()
	if len(efforts) != 2 || efforts[0] != "low" || efforts[1] != "high" {
		t.Fatalf("unexpected effort catalog: %v", efforts)
	}

	// A bare effort override (no model) still lands on the current route.
	agent.SetReasoningEffort("low")
	s2, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatalf("StartSession 2: %v", err)
	}
	defer s2.Close()
	waitFor(t, 5*time.Second, func() bool { return m.selectModelCount() >= 2 }, "second selectModel call")
	_, _, effort2 := m.selectModelAt(1)
	if effort2 != "low" {
		t.Fatalf("bare effort override must still apply, got %q", effort2)
	}
}

// TestAgent_ModeSwitcher_SettingsFallback verifies the mode switch degrades
// to settings.update when the deployment has no command registry (dsh npm
// builds like rc.6): the permission defaultPreset is written so NEW sessions
// start on the requested preset.
func TestAgent_ModeSwitcher_SettingsFallback(t *testing.T) {
	m := newMockDshServer()
	m.mu.Lock()
	m.failCommandExecute = true
	m.mu.Unlock()
	agent := newTestAgent(t, m, "/tmp/proj")

	s, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer s.Close()

	agent.SetMode("plan")
	if agent.GetMode() != "plan" {
		t.Fatalf("GetMode mismatch: %q", agent.GetMode())
	}
	waitFor(t, 5*time.Second, func() bool { return m.settingsUpdateCount() >= 1 }, "settings.update fallback")
	ns, patch := m.settingsUpdateAt(0)
	if ns != "permission" {
		t.Fatalf("expected permission namespace, got %q", ns)
	}
	if patch["defaultPreset"] != "read-only" {
		t.Fatalf("expected defaultPreset=read-only, got %v", patch)
	}
}
