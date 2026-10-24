package droid

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestLiveE2E_DroidResume(t *testing.T) {
	if os.Getenv("DROID_LIVE_E2E") != "1" {
		t.Skip("set DROID_LIVE_E2E=1 to use the installed, authenticated Droid CLI")
	}
	opts := map[string]any{"work_dir": t.TempDir()}
	if bin := os.Getenv("DROID_BIN"); bin != "" {
		opts["cmd"] = bin
	}
	if model := os.Getenv("DROID_MODEL"); model != "" {
		opts["model"] = model
	}
	a, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	s, err := a.StartSession(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	turn := func(prompt string) string {
		t.Helper()
		if err := s.Send(prompt, "", nil, nil); err != nil {
			t.Fatal(err)
		}
		var text strings.Builder
		for {
			select {
			case evt, ok := <-s.Events():
				if !ok {
					t.Fatal("Droid event stream closed before turn completion")
				}
				switch evt.Type {
				case core.EventText:
					text.WriteString(evt.Content)
				case core.EventError:
					t.Fatalf("Droid turn failed: %v", evt.Error)
				case core.EventResult:
					if evt.Content != "" {
						text.WriteString(evt.Content)
					}
					return text.String()
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
	}
	first := turn("Do not use tools. Remember the number 739182. Reply with exactly: remembered")
	if !strings.Contains(strings.ToLower(first), "remembered") {
		t.Fatalf("unexpected first reply: %q", first)
	}
	sid := s.CurrentSessionID()
	if sid == "" {
		t.Fatal("Droid did not report a session ID")
	}
	second := turn("Do not use tools. What number did I ask you to remember? Reply with just the number.")
	if !strings.Contains(second, "739182") {
		t.Fatalf("resumed session lost conversation context: %q", second)
	}
	if s.CurrentSessionID() != sid {
		t.Fatal("Droid changed session ID while resuming")
	}
	t.Logf("real Droid completed two turns and recalled 739182; session ID remained stable")
}
