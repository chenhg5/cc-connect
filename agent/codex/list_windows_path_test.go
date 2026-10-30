package codex

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseCodexSessionFile_MatchesWindowsSlashVariants(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout.jsonl")
	content := `{"type":"session_meta","payload":{"id":"session-slash","cwd":"D:/Codex/channels"}}` + "\n" +
		`{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"你好，这条对话是test-b"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	info := parseCodexSessionFile(path, `D:\Codex\channels`)
	if info == nil {
		t.Fatal("expected session to match slash-variant cwd")
	}
	if info.ID != "session-slash" {
		t.Fatalf("ID = %q", info.ID)
	}
}
