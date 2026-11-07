package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAgentListAllSessions_AggregatesWithoutRewriting(t *testing.T) {
	codexHome := t.TempDir()
	dir := filepath.Join(codexHome, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	alpha, beta := t.TempDir(), t.TempDir()
	oldTime := time.Unix(100, 0)
	newTime := time.Unix(200, 0)
	files := make(map[string]string)
	write := func(name, id, cwd, prompt string, modified time.Time) {
		t.Helper()
		encodedCwd, err := json.Marshal(cwd)
		if err != nil {
			t.Fatal(err)
		}
		body := `{"type":"session_meta","payload":{"id":"` + id + `","cwd":` + string(encodedCwd) + `,"source":"exec","originator":"codex_exec"}}` + "\n" +
			`{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"` + prompt + `"}]}}` + "\n"
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatal(err)
		}
		files[path] = body
	}
	write("rollout-a-alpha-id.jsonl", "alpha-id", alpha, "first alpha prompt", oldTime)
	write("rollout-b-alpha-id.jsonl", "alpha-id", alpha, "later alpha prompt", newTime)
	write("rollout-c-beta-id.jsonl", "beta-id", beta, "beta prompt", oldTime)

	agent := &Agent{workDir: alpha, codexHome: codexHome}
	sessions, err := agent.ListAllSessions(context.Background())
	if err != nil || len(sessions) != 2 {
		t.Fatalf("ListAllSessions = %+v, %v; want two logical sessions", sessions, err)
	}
	if got := sessions[0]; got.ID != "alpha-id" || got.WorkDir != alpha || got.Summary != "first alpha prompt" || !got.ModifiedAt.Equal(newTime) {
		t.Fatalf("aggregated alpha session = %+v", got)
	}
	if got := sessions[1]; got.ID != "beta-id" || got.WorkDir != beta {
		t.Fatalf("beta session = %+v", got)
	}
	for path, original := range files {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != original {
			t.Fatalf("global scan rewrote %s: %q, %v", path, got, err)
		}
	}
}
