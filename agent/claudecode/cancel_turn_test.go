package claudecode

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// recordingStdin captures what CancelTurn writes to the CLI's stdin.
type recordingStdin struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (r *recordingStdin) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(p)
}

func (r *recordingStdin) Close() error { return nil }

func (r *recordingStdin) lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(r.buf.String()), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func newCancelTestSession(t *testing.T, confirmTimeout time.Duration) (*claudeSession, *recordingStdin) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	stdin := &recordingStdin{}
	cs := &claudeSession{
		events:               make(chan core.Event, 8),
		ctx:                  ctx,
		stdin:                stdin,
		done:                 make(chan struct{}),
		cancelConfirmTimeout: confirmTimeout,
	}
	cs.sessionID.Store("cancel-test-session")
	cs.alive.Store(true)
	return cs, stdin
}

// resultEvent builds the raw CLI event that terminates a turn.
func interruptResultEvent() map[string]any {
	return map[string]any{
		"type":       "result",
		"subtype":    "error_during_execution",
		"is_error":   true,
		"result":     nil,
		"session_id": "cancel-test-session",
	}
}

// TestCancelTurn_WritesInterruptRequest pins the wire format. The engine's
// fallback decision depends on this being the exact control_request shape the
// CLI recognises; a silent change here would degrade /stop to a no-op again.
func TestCancelTurn_WritesInterruptRequest(t *testing.T) {
	cs, stdin := newCancelTestSession(t, 50*time.Millisecond)
	cs.turnActive.Store(true)

	if err := cs.CancelTurn(); err == nil {
		t.Fatal("CancelTurn returned nil; want the no-confirmation error")
	}

	lines := stdin.lines()
	if len(lines) != 1 {
		t.Fatalf("wrote %d lines, want 1: %q", len(lines), lines)
	}
	var req struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
		Request   struct {
			Subtype string `json:"subtype"`
		} `json:"request"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &req); err != nil {
		t.Fatalf("interrupt line is not valid JSON: %v (%q)", err, lines[0])
	}
	if req.Type != "control_request" {
		t.Errorf("type = %q, want control_request", req.Type)
	}
	if req.Request.Subtype != "interrupt" {
		t.Errorf("request.subtype = %q, want interrupt", req.Request.Subtype)
	}
	if !strings.HasPrefix(req.RequestID, "interrupt-") {
		t.Errorf("request_id = %q, want an interrupt- prefix", req.RequestID)
	}
}

// TestCancelTurn_ReturnsNilWhenResultConfirms is the happy path: the CLI emits
// the terminating result event, so the graceful cancel succeeded and the
// session stays alive for the next message.
func TestCancelTurn_ReturnsNilWhenResultConfirms(t *testing.T) {
	cs, _ := newCancelTestSession(t, 5*time.Second)
	cs.turnActive.Store(true)

	errCh := make(chan error, 1)
	go func() { errCh <- cs.CancelTurn() }()

	// CancelTurn must still be waiting — an unconfirmed cancel is not success.
	select {
	case err := <-errCh:
		t.Fatalf("CancelTurn returned %v before any result event; it must wait for confirmation", err)
	case <-time.After(150 * time.Millisecond):
	}

	cs.handleResult(interruptResultEvent())

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("CancelTurn = %v, want nil after the CLI confirmed the interrupt", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CancelTurn did not return after the result event")
	}
}

// TestCancelTurn_TimesOutWhenCLIEmitsNoResult is the production failure this
// whole change exists for. The CLI acknowledges the interrupt with a
// control_response but never terminates the turn; CancelTurn must surface that
// as an error so the engine falls back to Close() and actually kills the
// process. Returning nil here is what made /stop report success while the bot
// kept working.
func TestCancelTurn_TimesOutWhenCLIEmitsNoResult(t *testing.T) {
	cs, _ := newCancelTestSession(t, 100*time.Millisecond)
	cs.turnActive.Store(true)

	start := time.Now()
	err := cs.CancelTurn()
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("CancelTurn returned nil with no result event; the engine would keep the " +
			"session alive and the agent would keep running")
	}
	if !strings.Contains(err.Error(), "Close()") {
		t.Errorf("error %q should point the caller at the Close() fallback", err)
	}
	if elapsed < 100*time.Millisecond {
		t.Errorf("returned after %s, want to wait out the 100ms confirm timeout", elapsed)
	}
}

// TestCancelTurn_IdleSessionReturnsImmediately guards the /stop-on-an-idle-
// session path. No turn is running, so no result event is ever coming; waiting
// would add the full confirm timeout to every idle /stop.
func TestCancelTurn_IdleSessionReturnsImmediately(t *testing.T) {
	cs, _ := newCancelTestSession(t, 5*time.Second)
	// turnActive left false — the session has not sent a message.

	done := make(chan error, 1)
	go func() { done <- cs.CancelTurn() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("CancelTurn on an idle session = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("CancelTurn blocked on an idle session; there is no turn to confirm")
	}
}

// TestCancelTurn_CompactionResultDoesNotConfirm guards a subtle trap: a
// mid-turn compaction also arrives as `type:"result"`, but it explicitly is
// NOT turn completion (issue #481). Treating it as confirmation would make
// /stop report success while the turn is still running.
func TestCancelTurn_CompactionResultDoesNotConfirm(t *testing.T) {
	cs, _ := newCancelTestSession(t, 300*time.Millisecond)
	cs.turnActive.Store(true)

	errCh := make(chan error, 1)
	go func() { errCh <- cs.CancelTurn() }()

	time.Sleep(50 * time.Millisecond)
	cs.handleResult(map[string]any{
		"type":       "result",
		"subtype":    "compact",
		"session_id": "cancel-test-session",
	})

	select {
	case err := <-errCh:
		t.Fatalf("CancelTurn returned %v on a compaction event; compaction is not turn completion", err)
	case <-time.After(150 * time.Millisecond):
	}

	// Now the real terminator arrives and it must succeed.
	cs.handleResult(interruptResultEvent())
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("CancelTurn = %v, want nil after the true terminator", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CancelTurn did not return after the true terminator")
	}
}

// TestCancelTurn_ProcessExitWhileWaiting covers the other wake-up source: the
// process dies mid-wait, so waiting for a result event that can never arrive
// is pointless.
func TestCancelTurn_ProcessExitWhileWaiting(t *testing.T) {
	cs, _ := newCancelTestSession(t, 5*time.Second)
	cs.turnActive.Store(true)

	errCh := make(chan error, 1)
	go func() { errCh <- cs.CancelTurn() }()

	time.Sleep(50 * time.Millisecond)
	close(cs.done)

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("CancelTurn returned nil after the process exited")
		}
		if !strings.Contains(err.Error(), "exited") {
			t.Errorf("error %q should say the process exited", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CancelTurn did not return after the process exited")
	}
}

// TestTurnActiveTracksSendAndResult ties the idle fast-path to the flag that
// drives it: a delivered user message opens a turn, and the result event that
// terminates it closes the flag again.
func TestTurnActiveTracksSendAndResult(t *testing.T) {
	cs, _ := newCancelTestSession(t, time.Second)

	if cs.turnActive.Load() {
		t.Fatal("turnActive set before any message was sent")
	}
	if err := cs.Send("hello", "msg-1", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !cs.turnActive.Load() {
		t.Fatal("turnActive not set after a delivered user message")
	}
	cs.handleResult(map[string]any{
		"type":       "result",
		"subtype":    "success",
		"session_id": "cancel-test-session",
	})
	if cs.turnActive.Load() {
		t.Fatal("turnActive still set after the turn's result event")
	}
}
