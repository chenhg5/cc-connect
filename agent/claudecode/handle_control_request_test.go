package claudecode

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// nopWriteCloser swallows every write so handleControlRequest →
// RespondPermission → writeJSON → cs.stdin.Write does not panic in unit
// tests that don't stand up a real Claude subprocess. The lock mirrors
// claudeSession.stdinMu so concurrent tests don't trip the race detector.
type nopWriteCloser struct {
	mu sync.Mutex
}

func (n *nopWriteCloser) Write(p []byte) (int, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(p), nil
}

func (n *nopWriteCloser) Close() error { return nil }

// bufferWriteCloser wraps a *bytes.Buffer so it satisfies io.WriteCloser
// for tests that need to inspect what was written to cs.stdin.
type bufferWriteCloser struct {
	buf *bytes.Buffer
}

func (b *bufferWriteCloser) Write(p []byte) (int, error) { return b.buf.Write(p) }
func (b *bufferWriteCloser) Close() error                { return nil }

// String lets tests read back the captured payload in one call.
func (b *bufferWriteCloser) String() string { return b.buf.String() }

// newAskqControlRequest builds a can_use_tool control_request payload for
// AskUserQuestion with one question and two options — close to what Claude
// Code emits in the wild (issue #1820 reproducer).
func newAskqControlRequest(requestID string) map[string]any {
	return map[string]any{
		"type":       "control_request",
		"request_id": requestID,
		"request": map[string]any{
			"subtype":   "can_use_tool",
			"tool_name": "AskUserQuestion",
			"input": map[string]any{
				"questions": []any{
					map[string]any{
						"question": "Which database backend?",
						"header":   "Backend",
						"options": []any{
							map[string]any{"label": "Postgres", "description": "Use PostgreSQL"},
							map[string]any{"label": "MySQL", "description": "Use MySQL"},
						},
						"multiSelect": false,
					},
				},
			},
		},
	}
}

// newOtherToolControlRequest builds a can_use_tool payload for a non-AskUserQuestion
// tool (Bash) so we can assert the autoApprove short-circuit still applies.
func newOtherToolControlRequest(requestID string) map[string]any {
	return map[string]any{
		"type":       "control_request",
		"request_id": requestID,
		"request": map[string]any{
			"subtype":   "can_use_tool",
			"tool_name": "Bash",
			"input": map[string]any{
				"command":     "ls",
				"description": "list files",
			},
		},
	}
}

// newTestSessionForHandleControlRequest returns a claudeSession that is
// "alive" enough for handleControlRequest to drive without standing up a
// real subprocess: events channel buffered, alive flag set, stdin hooked
// to a no-op WriteCloser so RespondPermission doesn't panic, and
// ccHooks wired to an empty hook runner so the tryHook branch doesn't
// nil-deref.
func newTestSessionForHandleControlRequest(t *testing.T) *claudeSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cs := &claudeSession{
		events:  make(chan core.Event, 4),
		ctx:     ctx,
		stdin:   &nopWriteCloser{},
		ccHooks: newCCPermissionHookRunner(""), // empty workdir -> no hooks fire
	}
	cs.alive.Store(true)
	cs.sessionID.Store("test-session-1820")
	return cs
}

// newTestSessionWithCapturedStdin is like newTestSessionForHandleControlRequest
// but wires cs.stdin to a buffer-backed WriteCloser so the test can
// inspect what was sent to the (mocked) Claude subprocess.
func newTestSessionWithCapturedStdin(t *testing.T) (*claudeSession, *bufferWriteCloser) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	buf := &bufferWriteCloser{buf: &bytes.Buffer{}}
	cs := &claudeSession{
		events:  make(chan core.Event, 4),
		ctx:     ctx,
		stdin:   buf,
		ccHooks: newCCPermissionHookRunner(""),
	}
	cs.alive.Store(true)
	cs.sessionID.Store("test-session-1820")
	return cs, buf
}

// waitForEvent blocks up to 500ms for an event on ch. Returns (event, ok)
// where ok=false on timeout. Use this for control_request tests so a
// regression that drops the event on the floor fails the test loudly
// instead of hanging the suite.
func waitForEvent(t *testing.T, ch <-chan core.Event) (core.Event, bool) {
	t.Helper()
	select {
	case evt := <-ch:
		return evt, true
	case <-time.After(500 * time.Millisecond):
		return core.Event{}, false
	}
}

// TestHandleControlRequest_BypassAskQuestionForwardsEvent is the core
// regression test for issue #1820: in bypassPermissions mode, an
// AskUserQuestion request must NOT be auto-allowed by the adapter.
// Instead it must fall through to the events channel so the engine can
// render the interactive question card on Feishu/Lark/etc.
func TestHandleControlRequest_BypassAskQuestionForwardsEvent(t *testing.T) {
	cs := newTestSessionForHandleControlRequest(t)
	cs.autoApprove.Store(true) // bypassPermissions

	cs.handleControlRequest(newAskqControlRequest("req-1820-askq"))

	evt, ok := waitForEvent(t, cs.events)
	if !ok {
		t.Fatal("expected AskUserQuestion event on the events channel under bypassPermissions; got none within 500ms")
	}
	if evt.Type != core.EventPermissionRequest {
		t.Errorf("event Type = %q, want %q", evt.Type, core.EventPermissionRequest)
	}
	if evt.ToolName != "AskUserQuestion" {
		t.Errorf("event ToolName = %q, want %q", evt.ToolName, "AskUserQuestion")
	}
	if evt.RequestID != "req-1820-askq" {
		t.Errorf("event RequestID = %q, want %q", evt.RequestID, "req-1820-askq")
	}
	if len(evt.Questions) != 1 {
		t.Fatalf("event Questions len = %d, want 1", len(evt.Questions))
	}
	if evt.Questions[0].Question != "Which database backend?" {
		t.Errorf("event Questions[0].Question = %q, want %q", evt.Questions[0].Question, "Which database backend?")
	}
	if len(evt.Questions[0].Options) != 2 {
		t.Errorf("event Questions[0].Options len = %d, want 2", len(evt.Questions[0].Options))
	}
}

// TestHandleControlRequest_BypassOtherToolAutoAllows verifies that other
// (non-AskUserQuestion) tools still go through the existing auto-allow
// short-circuit under bypassPermissions — the fix for #1820 must not
// regress any other tool's behaviour.
func TestHandleControlRequest_BypassOtherToolAutoAllows(t *testing.T) {
	cs := newTestSessionForHandleControlRequest(t)
	cs.autoApprove.Store(true)

	cs.handleControlRequest(newOtherToolControlRequest("req-1820-bash"))

	// Should NOT reach the events channel — auto-allowed at the adapter.
	if _, ok := waitForEvent(t, cs.events); ok {
		t.Fatal("expected Bash under bypassPermissions to be auto-allowed without producing an event")
	}
}

// TestHandleControlRequest_DefaultModeForwardsAskQuestion is the
// counter-test for the bypass case: under mode=default (no autoApprove),
// AskUserQuestion must still flow through to the engine just like before.
func TestHandleControlRequest_DefaultModeForwardsAskQuestion(t *testing.T) {
	cs := newTestSessionForHandleControlRequest(t)
	cs.autoApprove.Store(false) // default mode
	cs.dontAsk.Store(false)

	cs.handleControlRequest(newAskqControlRequest("req-1820-default"))

	evt, ok := waitForEvent(t, cs.events)
	if !ok {
		t.Fatal("expected AskUserQuestion event on the events channel under default mode; got none within 500ms")
	}
	if evt.ToolName != "AskUserQuestion" {
		t.Errorf("event ToolName = %q, want %q", evt.ToolName, "AskUserQuestion")
	}
	if len(evt.Questions) != 1 {
		t.Errorf("event Questions len = %d, want 1", len(evt.Questions))
	}
}

// TestHandleControlRequest_DontAskStillDeniesAskQuestion confirms that
// dontAsk mode still denies AskUserQuestion (the #1820 fix only touches
// the autoApprove branch). Reporter explicitly noted that AskUserQuestion
// under dontAsk is a separate discussion; this test merely proves the
// current dontAsk contract is unchanged by the fix.
func TestHandleControlRequest_DontAskStillDeniesAskQuestion(t *testing.T) {
	cs := newTestSessionForHandleControlRequest(t)
	cs.autoApprove.Store(false)
	cs.dontAsk.Store(true)

	cs.handleControlRequest(newAskqControlRequest("req-1820-dontask"))

	if _, ok := waitForEvent(t, cs.events); ok {
		t.Fatal("expected dontAsk to short-circuit AskUserQuestion at the adapter (no engine event)")
	}
}

// TestHandleControlRequest_NonAskQuestionStillForwardsDefault checks the
// baseline default-mode behaviour for non-AskUserQuestion tools: they
// must still be forwarded to the engine when there are no hooks
// configured. This ensures the #1820 fix doesn't accidentally broaden
// any other branch.
func TestHandleControlRequest_NonAskQuestionStillForwardsDefault(t *testing.T) {
	cs := newTestSessionForHandleControlRequest(t)
	cs.autoApprove.Store(false)
	cs.dontAsk.Store(false)
	cs.acceptEditsOnly.Store(false)

	cs.handleControlRequest(newOtherToolControlRequest("req-1820-default-bash"))

	evt, ok := waitForEvent(t, cs.events)
	if !ok {
		t.Fatal("expected Bash event on the events channel under default mode (no autoApprove/dontAsk/acceptEditsOnly); got none within 500ms")
	}
	if evt.ToolName != "Bash" {
		t.Errorf("event ToolName = %q, want %q", evt.ToolName, "Bash")
	}
	if evt.RequestID != "req-1820-default-bash" {
		t.Errorf("event RequestID = %q, want %q", evt.RequestID, "req-1820-default-bash")
	}
}

// TestHandleControlRequest_BypassWritesAllowToStdin sanity-checks that
// the autoApprove allow path still ends up calling RespondPermission —
// we verify this indirectly by capturing the JSON written to cs.stdin.
// (AskUserQuestion must NOT do this under bypassPermissions.)
func TestHandleControlRequest_BypassWritesAllowToStdin(t *testing.T) {
	cs, buf := newTestSessionWithCapturedStdin(t)
	cs.autoApprove.Store(true)

	cs.handleControlRequest(newOtherToolControlRequest("req-1820-bash-stdin"))

	written := buf.String()
	if written == "" {
		t.Fatal("expected RespondPermission to write a control_response to stdin for Bash under bypassPermissions; got nothing")
	}
	// Quick sanity: the JSON should contain "allow" behaviour.
	if !bytes.Contains([]byte(written), []byte(`"allow"`)) {
		t.Errorf("expected allow behaviour in written JSON; got %q", written)
	}
	if !bytes.Contains([]byte(written), []byte("req-1820-bash-stdin")) {
		t.Errorf("expected request_id in written JSON; got %q", written)
	}
}

// TestHandleControlRequest_BypassAskQuestionDoesNotWriteStdin is the
// positive companion to the previous test: under bypassPermissions,
// AskUserQuestion MUST NOT write any control_response back to the Claude
// subprocess. Otherwise the tool would auto-resolve with empty input
// (the original #1820 bug) and the user would never see the question.
func TestHandleControlRequest_BypassAskQuestionDoesNotWriteStdin(t *testing.T) {
	cs, buf := newTestSessionWithCapturedStdin(t)
	cs.autoApprove.Store(true)

	cs.handleControlRequest(newAskqControlRequest("req-1820-askq-stdin"))

	if buf.String() != "" {
		t.Errorf("expected no stdin write for AskUserQuestion under bypassPermissions; got %q", buf.String())
	}
}

// drainChannel reads everything available on ch without blocking. Used in
// tests that need to assert "no event was emitted" without a fixed wait.
func drainChannel(ch <-chan core.Event) int {
	n := 0
	for {
		select {
		case <-ch:
			n++
		default:
			return n
		}
	}
}

var _ = drainChannel // referenced by future tests; keeps the helper available
