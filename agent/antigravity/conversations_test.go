package antigravity

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeAgyIndex(t *testing.T, cliDir string, rows [][]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(cliDir, "conversations"), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(cliDir, "conversation_summaries.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec("CREATE TABLE `conversation_summaries` (`conversation_id` text,`title` text NOT NULL DEFAULT \"\",`preview` text NOT NULL DEFAULT \"\",`step_count` integer NOT NULL DEFAULT 0,`last_modified_time` datetime NOT NULL,`workspace_uris` text NOT NULL,`parent_conversation_id` text NOT NULL DEFAULT \"\",PRIMARY KEY (`conversation_id`))"); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if _, err := db.Exec("INSERT INTO conversation_summaries (conversation_id,title,preview,step_count,last_modified_time,workspace_uris,parent_conversation_id) VALUES (?,?,?,?,?,?,?)", r...); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cliDir, "conversations", r[0].(string)+".db"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestListAgyConversations(t *testing.T) {
	cliDir := t.TempDir()
	writeAgyIndex(t, cliDir, [][]any{
		{"older", "First chat", "", 2, "2026-10-29 00:54:09.759877104+00:00", `["file:///work/proj"]`, ""},
		{"newer", "", "preview text", 14, "2026-10-29 03:15:22.851718509+00:00", `["file:///work/proj"]`, ""},
		{"other-dir", "Elsewhere", "", 2, "2026-10-29 03:43:24+00:00", `["file:///tmp"]`, ""},
		{"child", "Subagent", "", 1, "2026-10-29 03:50:00+00:00", `["file:///work/proj"]`, "newer"},
		{"deleted", "Gone", "", 1, "2026-10-29 03:55:00+00:00", `["file:///work/proj"]`, ""},
	})
	_ = os.Remove(filepath.Join(cliDir, "conversations", "deleted.db"))

	sessions, found, err := listAgyConversations(cliDir, "/work/proj/")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2: %+v", len(sessions), sessions)
	}
	if sessions[0].ID != "newer" || sessions[0].Summary != "preview text" || sessions[0].MessageCount != 14 {
		t.Fatalf("unexpected first session: %+v", sessions[0])
	}
	if sessions[1].ID != "older" || sessions[1].Summary != "First chat" {
		t.Fatalf("unexpected second session: %+v", sessions[1])
	}
	want := time.Date(2026, 10, 29, 3, 15, 22, 851718509, time.UTC)
	if !sessions[0].ModifiedAt.Equal(want) {
		t.Fatalf("ModifiedAt = %v, want %v", sessions[0].ModifiedAt, want)
	}
}

func TestListAgyConversations_NoIndex(t *testing.T) {
	_, found, err := listAgyConversations(t.TempDir(), "/work/proj")
	if found || err != nil {
		t.Fatalf("found=%v err=%v, want not found", found, err)
	}
}

func TestAgentListAndDeleteUseConversationStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workDir := t.TempDir()
	cliDir := filepath.Join(home, ".gemini", "antigravity-cli")
	writeAgyIndex(t, cliDir, [][]any{
		{"conv-1", "hello", "", 2, "2026-10-29 03:15:22+00:00", `["file://` + workDir + `"]`, ""},
	})

	a := &Agent{workDir: workDir, activeIdx: -1}
	sessions, err := a.ListSessions(context.Background())
	if err != nil || len(sessions) != 1 || sessions[0].ID != "conv-1" {
		t.Fatalf("ListSessions = %+v, %v", sessions, err)
	}
	if err := a.DeleteSession(context.Background(), "conv-1"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cliDir, "conversations", "conv-1.db")); !os.IsNotExist(err) {
		t.Fatalf("conversation file still exists: %v", err)
	}
	if err := a.DeleteSession(context.Background(), "../escape"); err == nil {
		t.Fatal("expected error for path-like session ID")
	}
}

func TestDetectNewAgyConversation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".gemini", "antigravity-cli", "conversations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "old.db"), nil, 0o600)
	pre := snapshotAgyConversations()
	start := time.Now()
	_ = os.WriteFile(filepath.Join(dir, "fresh.db"), nil, 0o600)

	if got := detectNewAgyConversation(pre, start); got != "fresh" {
		t.Fatalf("detectNewAgyConversation = %q, want fresh", got)
	}
}
