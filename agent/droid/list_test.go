package droid

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestGetSessionHistory_LastMessagesAndLocalTimestamps(t *testing.T) {
	home := t.TempDir()
	workDir := t.TempDir()
	dir := filepath.Join(home, ".factory", "sessions", droidSessionDirKeys(workDir)[0])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := strings.Join([]string{
		`{"type":"session_start","id":"history-session","cwd":"` + workDir + `"}`,
		`{"type":"message","message":{"role":"user","content":[{"type":"text","text":"` + strings.Repeat("a", 128*1024) + `"}]}}`,
		`{"type":"message","message":{"role":"user","visibility":"llm_only","content":[{"type":"text","text":"hidden context"}]}}`,
		`{"type":"message","timestamp":"2026-03-01T01:02:03.123Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"private"},{"type":"text","text":"first"},{"type":"text","text":"second"},{"type":"tool_use","name":"Read"}]}}`,
		`{"type":"message","message":{"role":"user","content":[{"type":"tool_result","content":"tool output"}]}}`,
		`{"type":"message","message":{"role":"user","content":"follow-up"}}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "history-session.jsonl"), []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &Agent{workDir: workDir, configEnv: []string{"HOME=" + home, "USERPROFILE=" + home}}
	sessions, err := a.ListSessions(context.Background())
	if err != nil || len(sessions) != 1 || sessions[0].ID != "history-session" {
		t.Fatalf("configured-home sessions = %+v, error = %v", sessions, err)
	}
	entries, err := a.GetSessionHistory(context.Background(), "history-session", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Role != "assistant" || entries[0].Content != "first\nsecond" || entries[1].Role != "user" || entries[1].Content != "follow-up" {
		t.Fatalf("history = %+v", entries)
	}
	wantTime, _ := time.Parse(time.RFC3339Nano, "2026-03-01T01:02:03.123Z")
	if !entries[0].Timestamp.Equal(wantTime) || entries[0].Timestamp.Location() != time.Local {
		t.Fatalf("timestamp = %v (%v)", entries[0].Timestamp, entries[0].Timestamp.Location())
	}
	if _, err := a.GetSessionHistory(context.Background(), "missing", 2); err == nil {
		t.Fatal("missing transcript must return an error")
	}
	a.SetWorkDir(t.TempDir())
	if _, err := a.GetSessionHistory(context.Background(), "history-session", 2); err == nil {
		t.Fatal("history must not expose a different workspace")
	}
}

type droidHistoryErrorReader struct{ err error }

func (r droidHistoryErrorReader) Read([]byte) (int, error) { return 0, r.err }

func TestReadDroidSessionHistory_RejectsPartialHistory(t *testing.T) {
	valid := `{"type":"message","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}` + "\n"
	want := errors.New("read failure")
	entries, err := readDroidSessionHistory(io.MultiReader(strings.NewReader(valid), droidHistoryErrorReader{err: want}), 0)
	if !errors.Is(err, want) || entries != nil {
		t.Fatalf("entries = %+v, error = %v; want no entries and read failure", entries, err)
	}
	entries, err = readDroidSessionHistory(strings.NewReader(valid+`{"type":"message",`), 0)
	if err == nil || entries != nil {
		t.Fatalf("corrupt transcript: entries = %+v, error = %v", entries, err)
	}
}
