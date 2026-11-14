package codex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateDesktopThreadIgnoresFilenameSubstringMatches(t *testing.T) {
	for _, nested := range []bool{false, true} {
		t.Run(fmt.Sprint(nested), func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			dir := filepath.Join(home, "sessions")
			if nested {
				dir = filepath.Join(dir, "2026", "11", "11")
			}
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			for name, id := range map[string]string{"rollout-000-thread-copy.jsonl": "thread-copy", "rollout-2026-thread.jsonl": "thread"} {
				data, err := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": id, "cwd": cwd, "source": "vscode"}})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, name), append(data, '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := (&Agent{codexHome: home, workDir: cwd}).ValidateDesktopThread("thread"); err != nil {
				t.Fatal("substring match shadowed the exact thread", err)
			}
			t.Setenv("CODEX_HOME", home)
			if err := (&Agent{workDir: cwd}).ValidateDesktopThread("thread"); err != nil {
				t.Fatal("default Codex home resolution regressed", err)
			}
		})
	}
}
