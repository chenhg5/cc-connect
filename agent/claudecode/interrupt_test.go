package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

type nopWriteCloser struct {
	io.Writer
}

func (n nopWriteCloser) Close() error { return nil }

type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) Snapshot() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

func TestClaudeSessionInterruptSession_WritesControlRequest(t *testing.T) {
	var buf safeBuffer
	cs := &claudeSession{
		stdin:  nopWriteCloser{Writer: &buf},
		ctx:    context.Background(),
		events: make(chan core.Event, 1),
	}
	cs.alive.Store(true)

	done := make(chan error, 1)
	go func() {
		done <- cs.InterruptSession(context.Background())
	}()

	var payload map[string]any
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := json.Unmarshal(bytes.TrimSpace(buf.Snapshot()), &payload); err == nil && len(payload) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(payload) == 0 {
		t.Fatal("expected control_request payload to be written")
	}
	reqID, _ := payload["request_id"].(string)
	cs.handleControlResponse(map[string]any{
		"response": map[string]any{
			"subtype":    "success",
			"request_id": reqID,
		},
	})
	cs.handleResult(map[string]any{
		"terminal_reason": "aborted_streaming",
	})

	if err := <-done; err != nil {
		t.Fatalf("InterruptSession: %v", err)
	}
	if got, _ := payload["type"].(string); got != "control_request" {
		t.Fatalf("type = %q, want control_request", got)
	}
	if reqID == "" {
		t.Fatal("request_id is empty")
	}
	req, _ := payload["request"].(map[string]any)
	if got, _ := req["subtype"].(string); got != "interrupt" {
		t.Fatalf("request.subtype = %q, want interrupt", got)
	}
}

func TestClaudeSessionInterruptSession_RequiresLiveSession(t *testing.T) {
	cs := &claudeSession{ctx: context.Background()}

	if err := cs.InterruptSession(context.Background()); err == nil {
		t.Fatal("expected error for non-running session")
	}
}

func TestClaudeSessionInterruptSession_WaitsForAbortedStreaming(t *testing.T) {
	var buf safeBuffer
	cs := &claudeSession{
		stdin:  nopWriteCloser{Writer: &buf},
		ctx:    context.Background(),
		events: make(chan core.Event, 1),
	}
	cs.alive.Store(true)

	done := make(chan error, 1)
	go func() {
		done <- cs.InterruptSession(context.Background())
	}()

	var payload map[string]any
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := json.Unmarshal(bytes.TrimSpace(buf.Snapshot()), &payload); err == nil && len(payload) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	reqID, _ := payload["request_id"].(string)
	cs.handleControlResponse(map[string]any{
		"response": map[string]any{
			"subtype":    "success",
			"request_id": reqID,
		},
	})
	time.Sleep(10 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("InterruptSession returned before interrupted result")
	default:
	}

	cs.handleResult(map[string]any{"terminal_reason": "aborted_streaming"})
	if err := <-done; err != nil {
		t.Fatalf("InterruptSession: %v", err)
	}
}

func TestClaudeSessionInterruptSession_WaitsForAbortedTools(t *testing.T) {
	var buf safeBuffer
	cs := &claudeSession{
		stdin:  nopWriteCloser{Writer: &buf},
		ctx:    context.Background(),
		events: make(chan core.Event, 1),
	}
	cs.alive.Store(true)

	done := make(chan error, 1)
	go func() {
		done <- cs.InterruptSession(context.Background())
	}()

	var payload map[string]any
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := json.Unmarshal(bytes.TrimSpace(buf.Snapshot()), &payload); err == nil && len(payload) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	reqID, _ := payload["request_id"].(string)
	cs.handleControlResponse(map[string]any{
		"response": map[string]any{
			"subtype":    "success",
			"request_id": reqID,
		},
	})
	cs.handleResult(map[string]any{"terminal_reason": "aborted_tools"})
	if err := <-done; err != nil {
		t.Fatalf("InterruptSession: %v", err)
	}
}

func TestClaudeSessionInterruptSession_FailsOnRejectedControlResponse(t *testing.T) {
	var buf safeBuffer
	cs := &claudeSession{
		stdin:  nopWriteCloser{Writer: &buf},
		ctx:    context.Background(),
		events: make(chan core.Event, 1),
	}
	cs.alive.Store(true)

	done := make(chan error, 1)
	go func() {
		done <- cs.InterruptSession(context.Background())
	}()

	var payload map[string]any
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := json.Unmarshal(bytes.TrimSpace(buf.Snapshot()), &payload); err == nil && len(payload) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	reqID, _ := payload["request_id"].(string)
	cs.handleControlResponse(map[string]any{
		"response": map[string]any{
			"subtype":    "error",
			"request_id": reqID,
			"error": map[string]any{
				"message": "cannot interrupt",
			},
		},
	})

	err := <-done
	if err == nil || !strings.Contains(err.Error(), "cannot interrupt") {
		t.Fatalf("InterruptSession error = %v, want rejected control response", err)
	}
}

func TestClaudeSessionInterruptSession_FailsOnNonInterruptedTerminalReason(t *testing.T) {
	var buf safeBuffer
	cs := &claudeSession{
		stdin:  nopWriteCloser{Writer: &buf},
		ctx:    context.Background(),
		events: make(chan core.Event, 1),
	}
	cs.alive.Store(true)

	done := make(chan error, 1)
	go func() {
		done <- cs.InterruptSession(context.Background())
	}()

	var payload map[string]any
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := json.Unmarshal(bytes.TrimSpace(buf.Snapshot()), &payload); err == nil && len(payload) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	reqID, _ := payload["request_id"].(string)
	cs.handleControlResponse(map[string]any{
		"response": map[string]any{
			"subtype":    "success",
			"request_id": reqID,
		},
	})
	cs.handleResult(map[string]any{"terminal_reason": "completed"})

	err := <-done
	if err == nil || !strings.Contains(err.Error(), "completed") {
		t.Fatalf("InterruptSession error = %v, want non-interrupted terminal reason", err)
	}
}

func TestClaudeSessionInterruptSession_TimesOutWaitingForAckOrCompletion(t *testing.T) {
	var buf safeBuffer
	cs := &claudeSession{
		stdin:  nopWriteCloser{Writer: &buf},
		ctx:    context.Background(),
		events: make(chan core.Event, 1),
	}
	cs.alive.Store(true)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := cs.InterruptSession(ctx)
	if err == nil || !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		t.Fatalf("InterruptSession error = %v, want deadline exceeded", err)
	}
}
