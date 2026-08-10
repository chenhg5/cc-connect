package claudecode

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regression: a JSONL line larger than the 256 KB scanner buffer used to
// silently truncate history — GetSessionHistory returned the entries parsed
// before the oversized line with a nil error. It must now surface
// bufio.ErrTooLong so callers know the result is incomplete.
func TestGetSessionHistory_OversizedLineReturnsError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workDir := t.TempDir()

	projectDir := filepath.Join(home, ".claude", "projects", encodeClaudeProjectKey(workDir))
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}

	sessionID := "oversize-session"
	path := filepath.Join(projectDir, sessionID+".jsonl")
	big := strings.Repeat("x", 300*1024) // > 256 KB scanner cap
	lines := []string{
		`{"type":"user","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","content":"` + big + `"}}`,
		`{"type":"assistant","timestamp":"2026-01-01T00:00:01Z","message":{"role":"assistant","content":"hi"}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	a := &Agent{workDir: workDir}
	_, err := a.GetSessionHistory(context.Background(), sessionID, 0)
	if err == nil {
		t.Fatalf("expected error when a line exceeds scanner buffer, got nil")
	}
	if !errors.Is(err, bufio.ErrTooLong) {
		t.Fatalf("expected bufio.ErrTooLong, got %v", err)
	}
}

// Regression: scanSessionMeta must not panic when the scanner hits
// ErrTooLong. It previously scanned a partial file and returned a misleading
// count/summary; the fix adds an error log and keeps returning the partial
// result (caller listSessions tolerates best-effort summaries).
func TestScanSessionMeta_OversizedLineDoesNotPanic(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "oversize.jsonl")

	big := strings.Repeat("y", 300*1024)
	lines := []string{
		`{"type":"user","message":{"content":"` + big + `"}}`,
		`{"type":"assistant","message":{"content":"hi"}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Must return without panicking; partial count is acceptable.
	summary, count := scanSessionMeta(path)
	if count < 0 {
		t.Fatalf("count must be non-negative, got %d", count)
	}
	_ = summary
}

// Sanity: happy path still parses fine after the scanner.Err() check is added.
func TestGetSessionHistory_HappyPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workDir := t.TempDir()

	projectDir := filepath.Join(home, ".claude", "projects", encodeClaudeProjectKey(workDir))
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}

	sessionID := "happy-session"
	path := filepath.Join(projectDir, sessionID+".jsonl")
	lines := []string{
		`{"type":"user","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","content":"hello"}}`,
		`{"type":"assistant","timestamp":"2026-01-01T00:00:01Z","message":{"role":"assistant","content":"hi"}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	a := &Agent{workDir: workDir}
	entries, err := a.GetSessionHistory(context.Background(), sessionID, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 history entries, got %d", len(entries))
	}
}
