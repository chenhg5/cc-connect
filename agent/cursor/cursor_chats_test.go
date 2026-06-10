package cursor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCursorChatsBaseDirs_XDGAndLegacy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))

	got := cursorChatsBaseDirs(home)
	want := []string{
		filepath.Join(home, "xdg", "Cursor", "chats"),
		filepath.Join(home, ".config", "Cursor", "chats"),
		filepath.Join(home, ".cursor", "chats"),
	}
	if len(got) != len(want) {
		t.Fatalf("cursorChatsBaseDirs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("cursorChatsBaseDirs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestListCursorSessions_ConfigCursorPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	workDir := filepath.Join(home, "project")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}

	hash := workspaceHash(workDir)
	sessionID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	chatsDir := filepath.Join(home, ".config", "Cursor", "chats", hash, sessionID)
	if err := os.MkdirAll(chatsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chatsDir, "store.db"), []byte("sqlite"), 0o644); err != nil {
		t.Fatal(err)
	}

	sessions, err := listCursorSessions(workDir)
	if err != nil {
		t.Fatalf("listCursorSessions() error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("listCursorSessions() len = %d, want 1", len(sessions))
	}
	if sessions[0].ID != sessionID {
		t.Fatalf("session ID = %q, want %q", sessions[0].ID, sessionID)
	}
}

func TestExtractCursorUserSummary_UserQuery(t *testing.T) {
	content := "<user_info>\nOS Version: darwin\n</user_info>\n\n<user_query>\n用一句话说明我们上次在做什么\n</user_query>"
	got := extractCursorUserSummary(content)
	want := "用一句话说明我们上次在做什么"
	if got != want {
		t.Fatalf("extractCursorUserSummary() = %q, want %q", got, want)
	}
}

func TestExtractCursorUserSummary_ArrayContent(t *testing.T) {
	content := []any{
		map[string]any{
			"type": "text",
			"text": "<user_query>\nlist\n</user_query>",
		},
	}
	got := extractCursorUserSummary(content)
	if got != "list" {
		t.Fatalf("extractCursorUserSummary() = %q, want %q", got, "list")
	}
}

func TestExtractCursorUserSummary_SkipsUserInfo(t *testing.T) {
	content := "<user_info>\nOS Version: darwin\n</user_info>"
	if got := extractCursorUserSummary(content); got != "" {
		t.Fatalf("extractCursorUserSummary() = %q, want empty", got)
	}
}

func TestCursorSessionSummary_PrefersNamedSession(t *testing.T) {
	got := cursorSessionSummary("CC Visibility", "", "175712b9-19d6-49eb-93b1-db74b9d598b3")
	if got != "CC Visibility" {
		t.Fatalf("cursorSessionSummary() = %q, want %q", got, "CC Visibility")
	}
}

func TestListCursorSessions_AlephPlatformHash(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("integration test requires local Cursor chat storage")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}

	workDir := "/Users/Ref/codes/aleph-platform"
	chatsDir := filepath.Join(home, ".config", "Cursor", "chats", workspaceHash(workDir))
	if _, err := os.Stat(chatsDir); err != nil {
		t.Skipf("aleph chat dir not present: %s", chatsDir)
	}

	sessions, err := listCursorSessions(workDir)
	if err != nil {
		t.Fatalf("listCursorSessions() error: %v", err)
	}
	if len(sessions) == 0 {
		t.Fatalf("expected aleph-platform sessions from %s", chatsDir)
	}
	for _, s := range sessions {
		if strings.HasSuffix(s.Summary, "...") && len(s.Summary) <= 15 {
			t.Errorf("session %s still shows opaque summary %q", s.ID, s.Summary)
		}
	}
	t.Logf("found %d sessions for aleph-platform", len(sessions))
	for _, s := range sessions {
		t.Logf("  %s -> %s", s.ID[:8], s.Summary)
	}
}
