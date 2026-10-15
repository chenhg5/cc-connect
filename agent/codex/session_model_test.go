package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// TestHelperFakeAppServer is not a real test: it is re-executed as a fake
// `codex app-server` by TestSessionModel_TurnStartUsesMappedModel.
func TestHelperFakeAppServer(t *testing.T) {
	if os.Getenv("CODEX_FAKE_APPSERVER") != "1" {
		t.Skip("helper process")
	}
	record := os.Getenv("CODEX_FAKE_TURN_FILE")
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for {
		var req struct {
			ID     any            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := dec.Decode(&req); err != nil {
			os.Exit(0)
		}
		if req.ID == nil {
			continue // notification
		}
		result := map[string]any{}
		switch req.Method {
		case "thread/start", "thread/resume":
			result = map[string]any{"thread": map[string]any{"id": "thread-1"}, "model": req.Params["model"]}
		case "turn/start":
			b, _ := json.Marshal(req.Params)
			f, _ := os.OpenFile(record, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			_, _ = f.Write(append(b, '\n'))
			_ = f.Close()
			result = map[string]any{"turn": map[string]any{"id": "turn-1"}}
		}
		_ = enc.Encode(map[string]any{"id": req.ID, "result": result})
	}
}

func TestSessionModel_TurnStartUsesMappedModel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell shim")
	}
	binDir := t.TempDir()
	script := "#!/bin/sh\nexec \"$CODEX_FAKE_TEST_BIN\" -test.run='^TestHelperFakeAppServer$'\n"
	if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(t.TempDir(), "turns.jsonl")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CODEX_FAKE_APPSERVER", "1")
	t.Setenv("CODEX_FAKE_TEST_BIN", os.Args[0])
	t.Setenv("CODEX_FAKE_TURN_FILE", record)

	ag, err := New(map[string]any{"work_dir": t.TempDir(), "backend": "app_server", "model": "gpt-6.1-sol"})
	if err != nil {
		t.Fatal(err)
	}
	a := ag.(*Agent)

	run := func(sessionModel string) core.AgentSession {
		a.SetSessionModel(sessionModel)
		s, err := a.StartSession(context.Background(), "")
		if err != nil {
			t.Fatalf("StartSession: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		if err := s.Send("hi", "", nil, nil); err != nil {
			t.Fatalf("Send: %v", err)
		}
		return s
	}
	luna := run("gpt-6-luna")
	sol := run("")

	b, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	var models []string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var p map[string]any
		if err := json.Unmarshal([]byte(line), &p); err != nil {
			t.Fatal(err)
		}
		m, _ := p["model"].(string)
		models = append(models, m)
	}
	if len(models) != 2 || models[0] != "gpt-6-luna" || models[1] != "gpt-6.1-sol" {
		t.Fatalf("turn/start models = %q, want [gpt-6-luna gpt-6.1-sol]", models)
	}
	// The reply footer reads the model from the live session.
	for _, c := range []struct {
		s    core.AgentSession
		want string
	}{{luna, "gpt-6-luna"}, {sol, "gpt-6.1-sol"}} {
		if got := c.s.(interface{ GetModel() string }).GetModel(); got != c.want {
			t.Fatalf("session GetModel() = %q, want %q", got, c.want)
		}
	}
	if got := a.GetModel(); got != "gpt-6.1-sol" {
		t.Fatalf("agent GetModel() = %q, want gpt-6.1-sol (override must not leak)", got)
	}
}
