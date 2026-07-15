package codex

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseCodexSession_NonMatchingCwdDoesNotReadTranscriptTail(t *testing.T) {
	reader := &tailGuardReader{first: []byte(`{"type":"session_meta","payload":{"id":"other-session","cwd":"/workspace/other"}}` + "\n")}

	info := parseCodexSession(reader, time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC), "/workspace/current")
	if info != nil {
		t.Fatalf("parseCodexSession() = %+v, want nil for a different cwd", info)
	}
	if reader.reads != 1 {
		t.Fatalf("parseCodexSession() read %d chunks, want only session metadata", reader.reads)
	}
}

func TestParseCodexSession_SessionMetaAfterMalformedLinePreservesSessionDetails(t *testing.T) {
	modifiedAt := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)
	transcript := strings.Join([]string{
		`not valid json`,
		`{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"write a focused test"}]}}`,
		`{"type":"session_meta","payload":{"id":"matching-session","cwd":"/workspace/current"}}`,
		`{"type":"response_item","payload":{"role":"assistant","content":[{"type":"output_text","text":"done"}]}}`,
	}, "\n")

	info := parseCodexSession(strings.NewReader(transcript), modifiedAt, "/workspace/current")
	if info == nil {
		t.Fatal("parseCodexSession() = nil, want matching session")
	}
	if info.ID != "matching-session" {
		t.Errorf("ID = %q, want matching-session", info.ID)
	}
	if info.Summary != "write a focused test" {
		t.Errorf("Summary = %q, want user prompt", info.Summary)
	}
	if info.MessageCount != 2 {
		t.Errorf("MessageCount = %d, want 2", info.MessageCount)
	}
	if !info.ModifiedAt.Equal(modifiedAt) {
		t.Errorf("ModifiedAt = %s, want %s", info.ModifiedAt, modifiedAt)
	}
}

func TestListCodexSessions_UsesCodexHomeAndOrdersSymlinkedTranscripts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	sessionsDir := filepath.Join(home, "sessions", "2026", "07", "15")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	workDir := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	older := time.Date(2026, time.July, 15, 10, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)

	linkedTarget := filepath.Join(t.TempDir(), "linked.jsonl")
	writeCodexSessionTranscript(t, linkedTarget, "linked-session", workDir)
	if err := os.Chtimes(linkedTarget, older, older); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(linkedTarget, filepath.Join(sessionsDir, "linked.jsonl")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	direct := filepath.Join(sessionsDir, "direct.jsonl")
	writeCodexSessionTranscript(t, direct, "direct-session", workDir)
	if err := os.Chtimes(direct, newer, newer); err != nil {
		t.Fatal(err)
	}

	sessions, err := listCodexSessions(workDir, "")
	if err != nil {
		t.Fatalf("listCodexSessions() error = %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("listCodexSessions() returned %d sessions, want 2: %+v", len(sessions), sessions)
	}
	if sessions[0].ID != "direct-session" || sessions[1].ID != "linked-session" {
		t.Errorf("session order = [%s %s], want [direct-session linked-session]", sessions[0].ID, sessions[1].ID)
	}
}

func writeCodexSessionTranscript(t *testing.T, path, id, cwd string) {
	t.Helper()
	content := `{"type":"session_meta","payload":{"id":"` + id + `","cwd":"` + cwd + `"}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

type tailGuardReader struct {
	first []byte
	reads int
}

func (r *tailGuardReader) Read(p []byte) (int, error) {
	r.reads++
	if r.reads == 1 {
		return copy(p, r.first), nil
	}
	return 0, errors.New("transcript tail must not be read")
}
