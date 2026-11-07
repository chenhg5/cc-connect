package droid

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestLiveE2E_DroidResume(t *testing.T) {
	if os.Getenv("DROID_LIVE_E2E") != "1" {
		t.Skip("set DROID_LIVE_E2E=1 to use the installed, authenticated Droid CLI")
	}
	workDir := t.TempDir()
	fixturePath := filepath.Join(workDir, "read-fixture.txt")
	const fixtureContent = "cc-connect-default-read-fixture-739182\n"
	if err := os.WriteFile(fixturePath, []byte(fixtureContent), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := map[string]any{"work_dir": workDir}
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
	var readTool string
	var sawReadResult bool
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
				case core.EventToolUse:
					if evt.ToolName == "Read" || (evt.ToolName == "Script" && strings.Contains(evt.ToolInput, "tools.Read")) {
						if strings.Contains(evt.ToolInput, "read-fixture.txt") {
							readTool = evt.ToolName
						}
					}
				case core.EventToolResult:
					if readTool != "" && evt.ToolName == readTool && evt.ToolSuccess != nil && *evt.ToolSuccess && strings.Contains(evt.ToolResult, strings.TrimSpace(fixtureContent)) {
						sawReadResult = true
					}
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
	// Send leaves exec.Cmd.Stdin nil, so this tool must finish with stdin at EOF.
	third := turn("Use the Read tool to read only read-fixture.txt in the current working directory. Do not use any other tool or read any other file. Reply with exactly the file contents.")
	if readTool == "" || !sawReadResult {
		t.Fatalf("default permissions did not produce a successful Read tool result: tool=%q, result=%v", readTool, sawReadResult)
	}
	if !strings.Contains(third, strings.TrimSpace(fixtureContent)) {
		t.Fatalf("Read reply omitted fixture contents: %q", third)
	}
	data, err := os.ReadFile(fixturePath)
	if err != nil || string(data) != fixtureContent {
		t.Fatalf("default Read changed fixture: %q, %v", data, err)
	}
	if s.CurrentSessionID() != sid {
		t.Fatal("Droid changed session ID while reading the fixture")
	}
	t.Log("real Droid recalled 739182 and read the fixture under default permissions with stdin EOF; session ID remained stable")
}
