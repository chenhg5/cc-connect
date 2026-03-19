package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPatchSessionSource(t *testing.T) {
	tmpDir := t.TempDir()

	sessionID := "test-session-abc123"
	sessionsDir := filepath.Join(tmpDir, ".codex", "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	fname := filepath.Join(sessionsDir, "rollout-"+sessionID+".jsonl")
	line1 := `{"timestamp":"2026-01-01T00:00:00Z","type":"session_meta","payload":{"id":"` + sessionID + `","source":"exec","originator":"codex_exec","cwd":"/tmp"}}`
	line2 := `{"timestamp":"2026-01-01T00:00:01Z","type":"response_item","payload":{"role":"user"}}`
	content := line1 + "\n" + line2 + "\n"

	if err := os.WriteFile(fname, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// Override CODEX_HOME for test
	t.Setenv("CODEX_HOME", filepath.Join(tmpDir, ".codex"))

	patchSessionSource(sessionID)

	data, err := os.ReadFile(fname)
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.SplitN(string(data), "\n", 2)

	if !strings.Contains(lines[0], `"source":"cli"`) {
		t.Errorf("expected source:cli, got first line: %s", lines[0])
	}
	if !strings.Contains(lines[0], `"originator":"codex_cli_rs"`) {
		t.Errorf("expected originator:codex_cli_rs, got first line: %s", lines[0])
	}
	if strings.Contains(lines[0], `"source":"exec"`) {
		t.Error("source:exec was not replaced")
	}

	// Second line should be untouched
	if !strings.HasPrefix(lines[1], `{"timestamp":"2026-01-01T00:00:01Z"`) {
		t.Errorf("second line was corrupted: %s", lines[1])
	}
}

func TestPatchSessionSource_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()
	sessionID := "test-idempotent-xyz"
	sessionsDir := filepath.Join(tmpDir, ".codex", "sessions")
	_ = os.MkdirAll(sessionsDir, 0o755)

	fname := filepath.Join(sessionsDir, "rollout-"+sessionID+".jsonl")
	line1 := `{"type":"session_meta","payload":{"id":"` + sessionID + `","source":"cli","originator":"codex_cli_rs"}}`
	if err := os.WriteFile(fname, []byte(line1+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CODEX_HOME", filepath.Join(tmpDir, ".codex"))

	patchSessionSource(sessionID)

	data, _ := os.ReadFile(fname)
	if string(data) != line1+"\n" {
		t.Errorf("file was modified when it shouldn't have been")
	}
}
