package codex

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regression: a JSONL line larger than the 256 KB scanner buffer used to
// silently truncate history — getSessionHistory returned whatever entries
// preceded the oversized line with a nil error. It must now surface
// bufio.ErrTooLong so callers know the result is incomplete.
func TestGetSessionHistory_OversizedLineReturnsError(t *testing.T) {
	tmpDir := t.TempDir()
	codexHome := filepath.Join(tmpDir, ".codex")
	sessionsDir := filepath.Join(codexHome, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	sessionID := "oversize-session"
	path := filepath.Join(sessionsDir, "rollout-"+sessionID+".jsonl")

	big := strings.Repeat("x", 300*1024) // > 256 KB scanner cap
	lines := []string{
		`{"timestamp":"2026-01-01T00:00:00Z","type":"session_meta","payload":{"id":"` + sessionID + `","cwd":"/tmp"}}`,
		`{"timestamp":"2026-01-01T00:00:01Z","type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"` + big + `"}]}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := getSessionHistory(sessionID, codexHome, 0)
	if err == nil {
		t.Fatalf("expected error when a line exceeds scanner buffer, got nil")
	}
	if !errors.Is(err, bufio.ErrTooLong) {
		t.Fatalf("expected bufio.ErrTooLong, got %v", err)
	}
}

// Regression: parseCodexSessionFile must not return a session with a
// misleading message count / summary when the underlying scanner hits
// ErrTooLong. Previously it returned the partial result silently; it must
// now return nil so listCodexSessions skips the unreadable file.
func TestParseCodexSessionFile_OversizedLineReturnsNil(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "rollout-oversize.jsonl")

	big := strings.Repeat("y", 300*1024)
	lines := []string{
		`{"type":"session_meta","payload":{"id":"s1","cwd":"/tmp"}}`,
		`{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"` + big + `"}]}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if got := parseCodexSessionFile(path, ""); got != nil {
		t.Fatalf("expected nil on oversized line, got session %+v (summary=%q count=%d)",
			got, got.Summary, got.MessageCount)
	}
}

// Sanity: the existing happy path still parses fine and returns both entries
// once the scanner err check is added.
func TestGetSessionHistory_HappyPath(t *testing.T) {
	tmpDir := t.TempDir()
	codexHome := filepath.Join(tmpDir, ".codex")
	sessionsDir := filepath.Join(codexHome, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	sessionID := "happy-session"
	path := filepath.Join(sessionsDir, "rollout-"+sessionID+".jsonl")
	lines := []string{
		`{"timestamp":"2026-01-01T00:00:00Z","type":"session_meta","payload":{"id":"` + sessionID + `","cwd":"/tmp"}}`,
		`{"timestamp":"2026-01-01T00:00:01Z","type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"hello"}]}}`,
		`{"timestamp":"2026-01-01T00:00:02Z","type":"response_item","payload":{"role":"assistant","content":[{"type":"output_text","text":"hi"}]}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	entries, err := getSessionHistory(sessionID, codexHome, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 history entries, got %d", len(entries))
	}
}
