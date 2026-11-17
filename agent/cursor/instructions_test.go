package cursor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestInstructionsFileReachesCursorOnEveryTurn(t *testing.T) {
	dir := t.TempDir()
	instructions := filepath.Join(dir, "AGENTS.md")
	cli := filepath.Join(dir, "cursor-agent")
	script := "#!/bin/sh\nfor arg do printf '%s\\n' \"$arg\"; done > \"$CURSOR_TEST_ARGS\"\nprintf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"ok\",\"session_id\":\"cursor-session\"}'\n"
	if err := os.WriteFile(cli, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	argsFile := filepath.Join(dir, "args")
	a, err := New(map[string]any{
		"cmd": cli, "work_dir": dir, "model": "fixture-alpha", "mode": "yolo",
		"instructions_file": instructions,
		"env":               map[string]any{"CURSOR_TEST_ARGS": argsFile},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := a.StartSession(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Errorf("close Cursor session: %v", err)
		}
	})
	for turn, rule := range []string{"Follow the project rules.", "Updated project rules."} {
		if err := os.WriteFile(instructions, []byte(rule), 0600); err != nil {
			t.Fatal(err)
		}
		if err := session.Send("User request", "", nil, nil); err != nil {
			t.Fatal(err)
		}
		waiting := true
		for waiting {
			select {
			case event := <-session.Events():
				if event.Type == core.EventError {
					t.Fatal(event.Error)
				}
				if event.Type == core.EventResult {
					waiting = false
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
		got, err := os.ReadFile(argsFile)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{rule, "User request", "--force", "--model\nfixture-alpha"} {
			if !strings.Contains(string(got), want) {
				t.Errorf("args %q do not contain %q", got, want)
			}
		}
		if turn == 0 && strings.Contains(string(got), "--resume") {
			t.Fatal("a new topic must start a new CLI session")
		}
		if turn == 1 && !strings.Contains(string(got), "--resume\ncursor-session") {
			t.Fatal("the next turn must resume the returned CLI session")
		}
	}
}

func TestMissingInstructionsFileRefusesTurn(t *testing.T) {
	dir := t.TempDir()
	cli := filepath.Join(dir, "cursor-agent")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	a, err := New(map[string]any{
		"cmd": cli, "work_dir": dir,
		"instructions_file": filepath.Join(dir, "missing"),
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := a.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Errorf("close Cursor session: %v", err)
		}
	})
	if err := session.Send("request", "", nil, nil); err == nil || !strings.Contains(err.Error(), "instructions") {
		t.Fatalf("missing managed instructions must fail explicitly, got %v", err)
	}
}
