package cursor

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGetSessionHistory_LastMessagesAndUserQuery(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 is required to build a Cursor chat fixture")
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	workDir := filepath.Join(home, "project")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionID := "11111111-2222-3333-4444-555555555555"
	dbPath := filepath.Join(home, ".config", "Cursor", "chats", workspaceHash(workDir), sessionID, "store.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}

	t1 := time.Date(2026, 11, 13, 9, 0, 0, 0, time.Local)
	t2 := t1.Add(time.Minute)
	t3 := t1.Add(2 * time.Minute)
	messages := []struct {
		role    string
		content any
		at      time.Time
	}{
		{role: "system", content: "system prompt"},
		{role: "user", content: "<user_info>\nOS Version: darwin\n</user_info>\n<user_query>\n第一问\n</user_query>", at: t1},
		{role: "assistant", content: "第一答", at: t1.Add(10 * time.Second)},
		{role: "user", content: "<user_info>\nignored\n</user_info>"},
		{role: "user", content: "第二问", at: t2},
		{role: "assistant", content: []any{map[string]any{"type": "text", "text": "第二答"}}, at: t2.Add(10 * time.Second)},
		{role: "user", content: "第三问", at: t3},
	}
	if err := writeCursorHistoryFixture(dbPath, messages); err != nil {
		t.Fatal(err)
	}

	a := &Agent{workDir: workDir}
	got, err := a.GetSessionHistory(t.Context(), sessionID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: %#v", len(got), got)
	}
	if got[0].Role != "assistant" || got[0].Content != "第二答" {
		t.Fatalf("first kept entry = %#v", got[0])
	}
	if got[1].Role != "user" || got[1].Content != "第三问" {
		t.Fatalf("last entry = %#v", got[1])
	}
	if !got[0].Timestamp.Equal(t2.Add(10*time.Second)) || !got[1].Timestamp.Equal(t3) {
		t.Fatalf("timestamps = %s, %s", got[0].Timestamp, got[1].Timestamp)
	}
	if got[0].Timestamp.After(got[1].Timestamp) {
		t.Fatal("history is not in chronological order")
	}

	all, err := a.GetSessionHistory(t.Context(), sessionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var userTexts []string
	for _, entry := range all {
		if entry.Role == "user" {
			userTexts = append(userTexts, entry.Content)
		}
	}
	if len(userTexts) != 3 || userTexts[0] != "第一问" || userTexts[1] != "第二问" || userTexts[2] != "第三问" {
		t.Fatalf("user texts = %#v", userTexts)
	}
}

func TestGetSessionHistory_MissingSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	a := &Agent{workDir: filepath.Join(home, "project")}
	_, err := a.GetSessionHistory(t.Context(), "missing-session", 10)
	if err == nil {
		t.Fatal("missing session must return an error")
	}
}

func writeCursorHistoryFixture(dbPath string, messages []struct {
	role    string
	content any
	at      time.Time
}) error {
	type blob struct {
		id   string
		body []byte
	}
	var blobs []blob
	var root []byte
	for i, msg := range messages {
		payload := map[string]any{"role": msg.role, "content": msg.content}
		if !msg.at.IsZero() {
			payload["createdAt"] = msg.at.Format(time.RFC3339Nano)
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		idBytes := make([]byte, 32)
		idBytes[31] = byte(i + 1)
		id := hex.EncodeToString(idBytes)
		blobs = append(blobs, blob{id: id, body: body})
		root = append(root, 0x0a, 0x20)
		root = append(root, idBytes...)
	}
	rootIDBytes := make([]byte, 32)
	rootIDBytes[0] = 0xff
	rootID := hex.EncodeToString(rootIDBytes)
	blobs = append(blobs, blob{id: rootID, body: root})

	meta, err := json.Marshal(map[string]string{"latestRootBlobId": rootID, "name": "fixture"})
	if err != nil {
		return err
	}

	sql := "CREATE TABLE meta (key TEXT, value TEXT);\nCREATE TABLE blobs (id TEXT, data BLOB);\n"
	sql += "INSERT INTO meta (key, value) VALUES ('0', '" + hex.EncodeToString(meta) + "');\n"
	for _, b := range blobs {
		sql += "INSERT INTO blobs (id, data) VALUES ('" + b.id + "', X'" + hex.EncodeToString(b.body) + "');\n"
	}
	cmd := exec.Command("sqlite3", dbPath)
	cmd.Stdin = strings.NewReader(sql)
	return cmd.Run()
}
