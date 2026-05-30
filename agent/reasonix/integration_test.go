package reasonix

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestIntegrationReasonixACP(t *testing.T) {
	if os.Getenv("CC_RUN_REASONIX_INTEGRATION") != "1" {
		t.Skip("set CC_RUN_REASONIX_INTEGRATION=1 to run Reasonix ACP integration")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	agent, err := New(map[string]any{
		"work_dir": t.TempDir(),
		"args":     []string{"acp", "--yolo"},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	session, err := agent.StartSession(ctx, "")
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	defer session.Close()

	if got := session.CurrentSessionID(); got == "" {
		t.Fatal("CurrentSessionID() is empty after ACP handshake")
	}

	if err := session.Send("Reply with exactly: reasonix-ok", nil, nil); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	var text strings.Builder
	for {
		select {
		case ev, ok := <-session.Events():
			if !ok {
				t.Fatalf("event channel closed before result; text=%q", text.String())
			}
			switch ev.Type {
			case core.EventText:
				text.WriteString(ev.Content)
			case core.EventResult:
				if !strings.Contains(strings.ToLower(text.String()), "reasonix-ok") {
					t.Fatalf("Reasonix response = %q, want reasonix-ok", text.String())
				}
				return
			case core.EventError:
				t.Fatalf("Reasonix emitted error: %v", ev.Error)
			}
		case <-ctx.Done():
			t.Fatalf("timeout waiting for Reasonix result; text=%q", text.String())
		}
	}
}
