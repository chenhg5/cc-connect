package acp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestAgent_HistoryProviderContracts(t *testing.T) {
	var _ core.HistoryProvider = (*Agent)(nil)
}

func TestAgent_GetSessionHistory_ReadsKimiTranscript(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KIMI_CODE_HOME", "")
	t.Setenv("KIMI_SHARE_DIR", "")

	sessionID := "session_acp_kimi_history"
	sessionDir := filepath.Join(home, ".kimi-code", "sessions", "wd_project", sessionID, "agents", "main")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	wire := strings.Join([]string{
		`{"type":"context.append_message","time":1700000000000,"message":{"role":"user","content":[{"type":"text","text":"first question"}],"origin":{"kind":"user"}}}`,
		`{"type":"context.append_loop_event","time":1700000000200,"event":{"type":"content.part","turnId":"turn-1","part":{"type":"text","text":"first reply"}}}`,
		`{"type":"context.append_message","time":1700000001000,"message":{"role":"user","content":[{"type":"text","text":"second question"}],"origin":{"kind":"user"}}}`,
		`{"type":"context.append_loop_event","time":1700000001200,"event":{"type":"content.part","turnId":"turn-2","part":{"type":"text","text":"second reply"}}}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(sessionDir, "wire.jsonl"), []byte(wire), 0o644); err != nil {
		t.Fatal(err)
	}

	a := &Agent{}
	entries, err := a.GetSessionHistory(context.Background(), sessionID, 0)
	if err != nil {
		t.Fatalf("GetSessionHistory error: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("len(entries) = %d, want 4", len(entries))
	}
	if entries[0].Role != "user" || entries[0].Content != "first question" {
		t.Fatalf("entries[0] = %+v, want user 'first question'", entries[0])
	}
	if entries[3].Role != "assistant" || entries[3].Content != "second reply" {
		t.Fatalf("entries[3] = %+v, want assistant 'second reply'", entries[3])
	}

	limited, err := a.GetSessionHistory(context.Background(), sessionID, 2)
	if err != nil {
		t.Fatalf("GetSessionHistory(limit=2) error: %v", err)
	}
	if len(limited) != 2 || limited[0].Content != "second question" || limited[1].Content != "second reply" {
		t.Fatalf("limited = %+v, want last 2 entries", limited)
	}
}

func TestAgent_GetSessionHistory_MissingSessionReturnsError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KIMI_CODE_HOME", "")
	t.Setenv("KIMI_SHARE_DIR", "")

	a := &Agent{}
	if _, err := a.GetSessionHistory(context.Background(), "nonexistent-session", 0); err == nil {
		t.Fatal("expected error for missing session, got nil")
	}
}

func TestAgent_GetSessionHistory_ForwardsAgentStorageEnv(t *testing.T) {
	home := t.TempDir()
	customHome := filepath.Join(home, "custom-kimi-home")
	t.Setenv("HOME", home)
	t.Setenv("KIMI_CODE_HOME", "")
	t.Setenv("KIMI_SHARE_DIR", "")

	sessionID := "session_acp_custom_home"
	sessionDir := filepath.Join(customHome, "sessions", "wd_project", sessionID, "agents", "main")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	wire := `{"type":"context.append_message","time":1700000000000,"message":{"role":"user","content":"from custom env"},"origin":{"kind":"user"}}` + "\n"
	if err := os.WriteFile(filepath.Join(sessionDir, "wire.jsonl"), []byte(wire), 0o644); err != nil {
		t.Fatal(err)
	}

	a := &Agent{}
	a.SetSessionEnv([]string{"KIMI_CODE_HOME=" + customHome})
	entries, err := a.GetSessionHistory(context.Background(), sessionID, 0)
	if err != nil {
		t.Fatalf("GetSessionHistory error: %v", err)
	}
	if len(entries) != 1 || entries[0].Content != "from custom env" {
		t.Fatalf("entries = %+v, want [from custom env]", entries)
	}
}

