package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateDesktopThreadExactMetadata(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "sessions"), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": "thread", "cwd": cwd, "source": "vscode"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "sessions", "rollout-thread.jsonl"), append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	a := &Agent{codexHome: home, workDir: cwd}
	if err := a.ValidateDesktopThread("thread"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"thr", "missing"} {
		if a.ValidateDesktopThread(id) == nil {
			t.Fatal("non-exact ID accepted")
		}
	}
	a.workDir = t.TempDir()
	if a.ValidateDesktopThread("thread") == nil {
		t.Fatal("wrong workspace accepted")
	}
}
