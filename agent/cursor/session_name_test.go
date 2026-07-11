package cursor

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestMeaningfulCursorSessionName(t *testing.T) {
	tests := map[string]bool{
		"":            false,
		"New Agent":   false,
		"new chat":    false,
		"Hccepose":    true,
		"CC Visibility": true,
	}
	for name, want := range tests {
		if got := meaningfulCursorSessionName(name); got != want {
			t.Errorf("meaningfulCursorSessionName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestWriteReadSessionDisplayName(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not available")
	}

	home := t.TempDir()
	t.Setenv("HOME", home)

	workDir := filepath.Join(home, "project")
	sessionID := "11111111-2222-3333-4444-555555555555"
	hash := workspaceHash(workDir)
	chatsDir := filepath.Join(home, ".cursor", "chats", hash, sessionID)
	if err := os.MkdirAll(chatsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(chatsDir, "store.db")

	meta := map[string]any{
		"agentId":          sessionID,
		"name":             "New Agent",
		"mode":             "default",
		"latestRootBlobId": "abc",
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	hexVal := hex.EncodeToString(raw)

	if out, err := exec.Command("sqlite3", dbPath,
		"CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT);",
		fmt.Sprintf("INSERT INTO meta(key,value) VALUES('0','%s');", hexVal),
	).CombinedOutput(); err != nil {
		t.Fatalf("setup db: %v: %s", err, out)
	}

	if err := writeSessionDisplayName(dbPath, "Feishu Sync Name"); err != nil {
		t.Fatalf("writeSessionDisplayName() error: %v", err)
	}
	got := readSessionMeta(dbPath)
	if got.Name != "Feishu Sync Name" {
		t.Fatalf("readSessionMeta().Name = %q, want %q", got.Name, "Feishu Sync Name")
	}

	agent := &Agent{workDir: workDir}
	name, err := agent.GetSessionDisplayName(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("GetSessionDisplayName() error: %v", err)
	}
	if name != "Feishu Sync Name" {
		t.Fatalf("GetSessionDisplayName() = %q, want %q", name, "Feishu Sync Name")
	}

	if err := agent.SetSessionDisplayName(context.Background(), sessionID, "Terminal Rename"); err != nil {
		t.Fatalf("SetSessionDisplayName() error: %v", err)
	}
	got = readSessionMeta(dbPath)
	if got.Name != "Terminal Rename" {
		t.Fatalf("after SetSessionDisplayName, name = %q, want %q", got.Name, "Terminal Rename")
	}
}
