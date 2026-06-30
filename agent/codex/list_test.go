package codex

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestListCodexSessions_DoesNotPatchSessionSource(t *testing.T) {
	workDir := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), ".codex")
	sessionID := "readonly-list-session"

	path := writeCodexListSession(t, codexHome, sessionID, workDir, "2026-01-01T00:00:00Z", time.Now())

	sessions, err := listCodexSessions(workDir, codexHome)
	if err != nil {
		t.Fatalf("listCodexSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("len(sessions) = %d, want 1", len(sessions))
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read session file: %v", err)
	}
	firstLine := strings.SplitN(string(data), "\n", 2)[0]
	if !strings.Contains(firstLine, `"source":"exec"`) {
		t.Fatalf("listCodexSessions modified session_meta source: %s", firstLine)
	}
	if !strings.Contains(firstLine, `"originator":"codex_exec"`) {
		t.Fatalf("listCodexSessions modified session_meta originator: %s", firstLine)
	}
}

func TestListCodexSessions_SortsByTranscriptTimestampNotFileMTime(t *testing.T) {
	workDir := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), ".codex")

	writeCodexListSession(t, codexHome, "older-transcript-newer-file", workDir, "2026-01-01T00:00:00Z", time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC))
	writeCodexListSession(t, codexHome, "newer-transcript-older-file", workDir, "2026-02-01T00:00:00Z", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))

	sessions, err := listCodexSessions(workDir, codexHome)
	if err != nil {
		t.Fatalf("listCodexSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("len(sessions) = %d, want 2", len(sessions))
	}
	if got, want := sessions[0].ID, "newer-transcript-older-file"; got != want {
		t.Fatalf("sessions[0].ID = %q, want %q", got, want)
	}
}

func writeCodexListSession(t *testing.T, codexHome, sessionID, workDir, timestamp string, mtime time.Time) string {
	t.Helper()

	sessionsDir := filepath.Join(codexHome, "sessions", "2026", "01", "01")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}

	path := filepath.Join(sessionsDir, "rollout-"+sessionID+".jsonl")
	lines := []string{
		fmt.Sprintf(`{"timestamp":%q,"type":"session_meta","payload":{"id":%q,"source":"exec","originator":"codex_exec","cwd":%q}}`, timestamp, sessionID, workDir),
		fmt.Sprintf(`{"timestamp":%q,"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"hello"}]}}`, timestamp),
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write session file: %v", err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes session file: %v", err)
	}
	return path
}
