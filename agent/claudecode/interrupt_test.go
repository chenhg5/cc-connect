package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"
)

type nopWriteCloser struct {
	io.Writer
}

func (n nopWriteCloser) Close() error { return nil }

func TestClaudeSessionInterruptSession_WritesControlRequest(t *testing.T) {
	var buf bytes.Buffer
	cs := &claudeSession{
		stdin: nopWriteCloser{Writer: &buf},
		ctx:   context.Background(),
	}
	cs.alive.Store(true)

	if err := cs.InterruptSession(context.Background()); err != nil {
		t.Fatalf("InterruptSession: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got, _ := payload["type"].(string); got != "control_request" {
		t.Fatalf("type = %q, want control_request", got)
	}
	if got, _ := payload["request_id"].(string); got == "" {
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
