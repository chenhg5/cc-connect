package droid

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListDroidSessionsFromBase(t *testing.T) {
	baseDir := t.TempDir()
	workDir := filepath.Join(baseDir, "repo")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workDir: %v", err)
	}
	absWorkDir, _ := filepath.Abs(workDir)

	keys := droidSessionDirKeys(absWorkDir)
	if len(keys) == 0 {
		t.Fatal("droidSessionDirKeys returned empty")
	}
	sessionDir := filepath.Join(baseDir, keys[0])
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir sessionDir: %v", err)
	}

	content := strings.Join([]string{
		`{"type":"session_start","id":"sid-1","title":"hello title","cwd":"` + absWorkDir + `"}`,
		`{"type":"message","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`,
		`{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":"world"}]}}`,
		"",
	}, "\n")

	path := filepath.Join(sessionDir, "sid-1.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write session file: %v", err)
	}

	sessions, err := listDroidSessionsFromBase(baseDir, workDir)
	if err != nil {
		t.Fatalf("listDroidSessionsFromBase error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("len(sessions) = %d, want 1", len(sessions))
	}
	if sessions[0].ID != "sid-1" {
		t.Fatalf("session ID = %q, want sid-1", sessions[0].ID)
	}
	if sessions[0].Summary != "hello title" {
		t.Fatalf("summary = %q, want hello title", sessions[0].Summary)
	}
	if sessions[0].MessageCount != 2 {
		t.Fatalf("message count = %d, want 2", sessions[0].MessageCount)
	}
}

func TestFindDroidSessionFileFromBase(t *testing.T) {
	baseDir := t.TempDir()
	workDir := filepath.Join(baseDir, "repo")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workDir: %v", err)
	}
	absWorkDir, _ := filepath.Abs(workDir)

	keys := droidSessionDirKeys(absWorkDir)
	sessionDir := filepath.Join(baseDir, keys[0])
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir sessionDir: %v", err)
	}

	path := filepath.Join(sessionDir, "sid-2.jsonl")
	content := `{"type":"session_start","id":"sid-2","title":"t","cwd":"` + absWorkDir + `"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write session file: %v", err)
	}

	got, err := findDroidSessionFileFromBase(baseDir, workDir, "sid-2")
	if err != nil {
		t.Fatalf("findDroidSessionFileFromBase error: %v", err)
	}
	if got != path {
		t.Fatalf("find path = %q, want %q", got, path)
	}
}
