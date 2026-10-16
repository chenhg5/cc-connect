package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSessionInstructions_ThreadParamsCarryFileText(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell shim")
	}
	binDir := t.TempDir()
	script := "#!/bin/sh\nexec \"$CODEX_FAKE_TEST_BIN\" -test.run='^TestHelperFakeAppServer$'\n"
	if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	threads := filepath.Join(t.TempDir(), "threads.jsonl")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CODEX_FAKE_APPSERVER", "1")
	t.Setenv("CODEX_FAKE_TEST_BIN", os.Args[0])
	t.Setenv("CODEX_FAKE_TURN_FILE", filepath.Join(t.TempDir(), "turns.jsonl"))
	t.Setenv("CODEX_FAKE_THREAD_FILE", threads)

	ag, err := New(map[string]any{"work_dir": t.TempDir(), "backend": "app_server"})
	if err != nil {
		t.Fatal(err)
	}
	a := ag.(*Agent)

	start := func(text, resumeID string) {
		a.SetSessionInstructions("/p/persona.md", text)
		if text == "" {
			a.SetSessionInstructions("", "")
		}
		s, err := a.StartSession(context.Background(), resumeID)
		if err != nil {
			t.Fatalf("StartSession: %v", err)
		}
		_ = s.Close()
	}
	start("你是龙娘。", "")         // mapped, new thread
	start("你是龙娘。", "thread-9") // mapped, resumed thread
	start("", "")              // not mapped: built-in prompt

	b, err := os.ReadFile(threads)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	want := []struct{ method, base string }{
		{"thread/start", "你是龙娘。"},
		{"thread/resume", "你是龙娘。"},
		{"thread/start", ""},
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d thread requests, want %d: %s", len(lines), len(want), b)
	}
	for i, line := range lines {
		var r struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatal(err)
		}
		base, has := r.Params["baseInstructions"].(string)
		if r.Method != want[i].method || base != want[i].base || (want[i].base == "" && has) {
			t.Fatalf("request %d = %s baseInstructions=%q (present=%v), want %s %q", i, r.Method, base, has, want[i].method, want[i].base)
		}
	}

}

func TestSessionInstructions_ExecArgs(t *testing.T) {
	cs, err := newCodexSession(context.Background(), "codex", nil, "/tmp", "", "", "", "", "", nil, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	cs.instructionsFile = "/x/persona.md"
	args := strings.Join(cs.buildExecArgs("hi", nil), " ")
	if !strings.Contains(args, `-c model_instructions_file="/x/persona.md"`) {
		t.Fatalf("exec args missing instructions override: %s", args)
	}
}
