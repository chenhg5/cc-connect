package copilot

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestIntegration_CopilotFlow(t *testing.T) {
	if os.Getenv("COPILOT_INTEGRATION") != "1" {
		t.Skip("set COPILOT_INTEGRATION=1 to run")
	}
	if _, err := exec.LookPath("copilot"); err != nil {
		t.Skip("copilot CLI not in PATH, skipping")
	}

	agent, err := New(map[string]any{
		"work_dir": t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	session, err := agent.StartSession(ctx, "integration-test")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	// Send a deterministic prompt
	err = session.Send("Reply with exactly 'integration-ok' and nothing else", nil, nil)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Collect events until done
	var result string
	timeout := time.After(25 * time.Second)
	for {
		select {
		case ev, ok := <-session.Events():
			if !ok {
				goto done
			}
			switch ev.Type {
			case core.EventText:
				result += ev.Content
			case core.EventResult:
				result += ev.Content
				goto done
			}
		case <-timeout:
			t.Fatal("timeout waiting for response")
		}
	}
done:
	t.Logf("Response: %s", result)
	if !strings.Contains(strings.ToLower(result), "integration-ok") {
		t.Errorf("expected 'integration-ok' in response, got: %s", result)
	}
}
