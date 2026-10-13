package acp

import (
	"context"
	"github.com/chenhg5/cc-connect/core"
	"os"
	"os/exec"
	"testing"
	"time"
)

// Opt-in, isolated connectivity request; never resumes a production session.
func TestTraeQueueIntegration(t *testing.T) {
	if os.Getenv("CC_TRAE_QUEUE_INTEGRATION") != "1" {
		t.Skip("opt-in local Trae connectivity check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	s, err := newACPSession(ctx, acpSessionConfig{command: "traecli", args: []string{"--permission-mode", "default", "acp", "serve"}, workDir: t.TempDir(), initialModel: "gpt-6-astra", queueDiagnostics: true})
	if err != nil {
		t.Fatal(err)
	}
	id := s.CurrentSessionID()
	defer func() {
		s.Close()
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if err := exec.CommandContext(cleanup, "traecli", "delete", "--force", id).Run(); err != nil {
			t.Errorf("delete owned diagnostic session: %v", err)
		}
	}()
	done := make(chan error, 1)
	go func() {
		done <- s.Send("Connectivity diagnostic only. Reply exactly OK. Do not call tools, read or modify files.", "", nil, nil)
	}()
	for {
		select {
		case ev := <-s.Events():
			switch ev.Type {
			case core.EventProgress:
				if ev.Progress != nil && ev.Progress.Phase == "waiting_model" {
					t.Logf("native queue signal reached engine event, position=%d", ev.Progress.QueuePosition)
					return
				}
			case core.EventResult:
				if ev.Done {
					t.Log("model completed; no queue encountered")
					return
				}
			case core.EventError:
				t.Fatalf("probe failed: %v", ev.Error)
			case core.EventPermissionRequest:
				t.Fatal("unexpected tool permission request")
			}
		case <-ctx.Done():
			t.Fatal("no queue signal or completion within diagnostic deadline")
		}
	}
}
