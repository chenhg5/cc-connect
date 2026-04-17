package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListCodexSessions_MatchesSymlinkedWorkDir(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("CODEX_HOME", filepath.Join(tmpHome, ".codex"))

	realDir := filepath.Join(tmpHome, "real-project")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(tmpHome, "link-project")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Skip("symlinks not supported")
	}

	sessionsDir := filepath.Join(tmpHome, ".codex", "sessions", "2026", "04", "17")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sessionFile := filepath.Join(sessionsDir, "rollout-test-thread.jsonl")
	content := strings.Join([]string{
		`{"type":"session_meta","payload":{"id":"thread-1","cwd":"` + realDir + `"}}`,
		`{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"hello"}]}}`,
	}, "\n")
	if err := os.WriteFile(sessionFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	a, err := New(map[string]any{"work_dir": linkDir})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	sessions, err := a.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != "thread-1" {
		t.Fatalf("sessions = %+v, want thread-1", sessions)
	}
}
