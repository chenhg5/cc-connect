package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeSessionFileAt marshals sessionFileData to a JSON file at the given absolute path,
// creating parent directories as needed.
func writeSessionFileAt(t *testing.T, path string, fd sessionFileData) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(fd)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func newTestSessionFileData(sessionKey, agentSessionID string) sessionFileData {
	return sessionFileData{
		Sessions: map[string]*sessionData{
			"s1": {
				ID:             "s1",
				AgentSessionID: agentSessionID,
			},
		},
		ActiveSession: map[string]string{
			sessionKey: "s1",
		},
		UserSessions: map[string][]string{
			sessionKey: {"s1"},
		},
	}
}

func TestFindAgentSessionID_PlainFilename(t *testing.T) {
	dir := t.TempDir()
	writeSessionFileAt(t,
		filepath.Join(dir, "sessions", "mybot.json"),
		newTestSessionFileData("discord:111:222", "uuid-plain"),
	)

	got, err := findAgentSessionID(dir, "mybot", "discord:111:222")
	if err != nil {
		t.Fatal(err)
	}
	if got != "uuid-plain" {
		t.Fatalf("got %q, want uuid-plain", got)
	}
}

func TestFindAgentSessionID_HashedFilename(t *testing.T) {
	dir := t.TempDir()
	writeSessionFileAt(t,
		filepath.Join(dir, "sessions", "mybot_a1b2c3d4.json"),
		newTestSessionFileData("discord:111:222", "uuid-hashed"),
	)

	got, err := findAgentSessionID(dir, "mybot", "discord:111:222")
	if err != nil {
		t.Fatal(err)
	}
	if got != "uuid-hashed" {
		t.Fatalf("got %q, want uuid-hashed", got)
	}
}

func TestFindAgentSessionID_WorkspaceFilename(t *testing.T) {
	dir := t.TempDir()
	writeSessionFileAt(t,
		filepath.Join(dir, "sessions", "mybot_ws_abcd1234.json"),
		newTestSessionFileData("discord:111:222", "uuid-ws"),
	)

	got, err := findAgentSessionID(dir, "mybot", "discord:111:222")
	if err != nil {
		t.Fatal(err)
	}
	if got != "uuid-ws" {
		t.Fatalf("got %q, want uuid-ws", got)
	}
}

func TestFindAgentSessionID_LegacyPath(t *testing.T) {
	dir := t.TempDir()
	// Legacy: file directly in dataDir, not in sessions/ subdir
	writeSessionFileAt(t,
		filepath.Join(dir, "mybot.json"),
		newTestSessionFileData("discord:111:222", "uuid-legacy"),
	)

	got, err := findAgentSessionID(dir, "mybot", "discord:111:222")
	if err != nil {
		t.Fatal(err)
	}
	if got != "uuid-legacy" {
		t.Fatalf("got %q, want uuid-legacy", got)
	}
}

func TestFindAgentSessionID_LegacySessionsJsonNaming(t *testing.T) {
	dir := t.TempDir()
	writeSessionFileAt(t,
		filepath.Join(dir, "mybot.sessions.json"),
		newTestSessionFileData("discord:111:222", "uuid-legacy-naming"),
	)

	got, err := findAgentSessionID(dir, "mybot", "discord:111:222")
	if err != nil {
		t.Fatal(err)
	}
	if got != "uuid-legacy-naming" {
		t.Fatalf("got %q, want uuid-legacy-naming", got)
	}
}

func TestFindAgentSessionID_MultipleFiles_CorrectMatch(t *testing.T) {
	dir := t.TempDir()
	sessDir := filepath.Join(dir, "sessions")

	// File 1: contains discord key
	writeSessionFileAt(t,
		filepath.Join(sessDir, "mybot_ws_aaaa1111.json"),
		newTestSessionFileData("discord:111:222", "uuid-discord"),
	)
	// File 2: contains telegram key (different session key)
	writeSessionFileAt(t,
		filepath.Join(sessDir, "mybot_ws_bbbb2222.json"),
		newTestSessionFileData("telegram:333:444", "uuid-telegram"),
	)

	// Should find discord in file 1
	got, err := findAgentSessionID(dir, "mybot", "discord:111:222")
	if err != nil {
		t.Fatal(err)
	}
	if got != "uuid-discord" {
		t.Fatalf("got %q, want uuid-discord", got)
	}

	// Should find telegram in file 2
	got, err = findAgentSessionID(dir, "mybot", "telegram:333:444")
	if err != nil {
		t.Fatal(err)
	}
	if got != "uuid-telegram" {
		t.Fatalf("got %q, want uuid-telegram", got)
	}
}

func TestFindAgentSessionID_NoActiveSession(t *testing.T) {
	dir := t.TempDir()
	writeSessionFileAt(t,
		filepath.Join(dir, "sessions", "mybot.json"),
		sessionFileData{
			Sessions:      map[string]*sessionData{},
			ActiveSession: map[string]string{},
		},
	)

	_, err := findAgentSessionID(dir, "mybot", "discord:111:222")
	if err == nil {
		t.Fatal("expected error for missing session key")
	}
}

func TestFindAgentSessionID_EmptyAgentSessionID(t *testing.T) {
	dir := t.TempDir()
	writeSessionFileAt(t,
		filepath.Join(dir, "sessions", "mybot.json"),
		newTestSessionFileData("discord:111:222", ""),
	)

	_, err := findAgentSessionID(dir, "mybot", "discord:111:222")
	if err == nil {
		t.Fatal("expected error for empty agent session ID")
	}
}

func TestFindAgentSessionID_NoSessionFile(t *testing.T) {
	dir := t.TempDir()

	_, err := findAgentSessionID(dir, "nonexistent", "discord:111:222")
	if err == nil {
		t.Fatal("expected error for missing session file")
	}
}

func TestMatchesProject(t *testing.T) {
	tests := []struct {
		filename string
		project  string
		want     bool
	}{
		{"mybot.json", "mybot", true},
		{"mybot_abc123.json", "mybot", true},          // hash suffix
		{"mybot_ws_abc123.json", "mybot", true},        // workspace hash suffix
		{"mybot.sessions.json", "mybot", true},         // legacy naming
		{"other.json", "mybot", false},                 // different project
		{"mybotextra.json", "mybot", false},             // no underscore separator
		{"mybot.txt", "mybot", false},                  // wrong extension
		{"mybot_extra.json", "mybot", false},            // suffix is not hex
		{"mybot_ws_notahex.json", "mybot", false},       // ws_ prefix but non-hex suffix
		{"mybot_AABB00.json", "mybot", true},            // uppercase hex
		{"mybot_ws.json", "mybot", false},               // "ws" alone is not hex (Codex #1 fix)
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			got := matchesProject(tt.filename, tt.project)
			if got != tt.want {
				t.Fatalf("matchesProject(%q, %q) = %v, want %v", tt.filename, tt.project, got, tt.want)
			}
		})
	}
}

func TestFindAgentSessionID_EmptyAgentID_ReturnsSpecificError(t *testing.T) {
	dir := t.TempDir()
	writeSessionFileAt(t,
		filepath.Join(dir, "sessions", "mybot.json"),
		newTestSessionFileData("discord:111:222", ""),
	)

	_, err := findAgentSessionID(dir, "mybot", "discord:111:222")
	if err == nil {
		t.Fatal("expected error for empty agent session ID")
	}
	// Should get a specific error, not the generic "no session found" message
	if strings.Contains(err.Error(), "no session found") {
		t.Fatalf("expected specific error, got generic: %v", err)
	}
}

func TestFindAgentSessionID_DuplicateKey_PrefersNewerUpdatedAt(t *testing.T) {
	dir := t.TempDir()
	sessDir := filepath.Join(dir, "sessions")

	oldTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	newTime := time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC)

	// File 1: same session key, older UpdatedAt
	writeSessionFileAt(t,
		filepath.Join(sessDir, "mybot_ws_aaaa1111.json"),
		sessionFileData{
			Sessions: map[string]*sessionData{
				"s1": {ID: "s1", AgentSessionID: "uuid-old", UpdatedAt: oldTime},
			},
			ActiveSession: map[string]string{"discord:111:222": "s1"},
			UserSessions:  map[string][]string{"discord:111:222": {"s1"}},
		},
	)
	// File 2: same session key, newer UpdatedAt
	writeSessionFileAt(t,
		filepath.Join(sessDir, "mybot_ws_bbbb2222.json"),
		sessionFileData{
			Sessions: map[string]*sessionData{
				"s1": {ID: "s1", AgentSessionID: "uuid-new", UpdatedAt: newTime},
			},
			ActiveSession: map[string]string{"discord:111:222": "s1"},
			UserSessions:  map[string][]string{"discord:111:222": {"s1"}},
		},
	)

	got, err := findAgentSessionID(dir, "mybot", "discord:111:222")
	if err != nil {
		t.Fatal(err)
	}
	if got != "uuid-new" {
		t.Fatalf("got %q, want uuid-new (should prefer newer UpdatedAt)", got)
	}
}
