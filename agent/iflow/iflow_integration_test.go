package iflow

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestIFlowSessionIntegration(t *testing.T) {
	if os.Getenv("IFLOW_INTEGRATION") == "" {
		t.Skip("set IFLOW_INTEGRATION=1 to run")
	}
	if _, err := exec.LookPath("iflow"); err != nil {
		t.Skip("iflow CLI not found in PATH")
	}

	a, err := New(map[string]any{"work_dir": "/tmp"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	sess, err := a.StartSession(context.Background(), "")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer sess.Close()

	if err := sess.Send("Reply exactly READY", nil); err != nil {
		t.Fatalf("Send #1: %v", err)
	}
	res1 := waitForResult(t, sess.Events())
	if !strings.Contains(strings.ToUpper(res1.Content), "READY") {
		t.Fatalf("unexpected first result: %q", res1.Content)
	}
	sid := sess.CurrentSessionID()
	if sid == "" {
		t.Fatal("expected session id after first send")
	}

	if err := sess.Send("Reply exactly RESUMED", nil); err != nil {
		t.Fatalf("Send #2: %v", err)
	}
	res2 := waitForResult(t, sess.Events())
	if !strings.Contains(strings.ToUpper(res2.Content), "RESUMED") {
		t.Fatalf("unexpected second result: %q", res2.Content)
	}
	if sid2 := sess.CurrentSessionID(); sid2 != sid {
		t.Fatalf("session id changed: %q -> %q", sid, sid2)
	}

	resumed, err := a.StartSession(context.Background(), sid)
	if err != nil {
		t.Fatalf("StartSession resume: %v", err)
	}
	defer resumed.Close()
	if err := resumed.Send("Reply exactly CONTINUED", nil); err != nil {
		t.Fatalf("Send resume: %v", err)
	}
	res3 := waitForResult(t, resumed.Events())
	if !strings.Contains(strings.ToUpper(res3.Content), "CONTINUED") {
		t.Fatalf("unexpected resumed result: %q", res3.Content)
	}
	if sid3 := resumed.CurrentSessionID(); sid3 != sid {
		t.Fatalf("resumed session id mismatch: want %q, got %q", sid, sid3)
	}
}

func waitForResult(t *testing.T, ch <-chan core.Event) core.Event {
	t.Helper()
	timeout := time.After(90 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("events channel closed")
			}
			switch ev.Type {
			case core.EventError:
				t.Fatalf("event error: %v", ev.Error)
			case core.EventResult:
				fmt.Printf("[RESULT] sid=%s content=%s\n", ev.SessionID, ev.Content)
				return ev
			}
		case <-timeout:
			t.Fatal("timeout waiting for result")
		}
	}
}
