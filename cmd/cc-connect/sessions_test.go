package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestParseSessionKey(t *testing.T) {
	tests := []struct {
		key          string
		wantPlatform string
		wantGroup    string
	}{
		{"feishu:oc_xxx:ou_yyy", "feishu", "oc_xxx:ou_yyy"},
		{"telegram:123:456", "telegram", "123:456"},
		{"discord:guild123", "discord", "guild123"},
		{"nocolon", "nocolon", ""},
		{"slack:", "slack", ""},
		{":empty", "", "empty"},
	}

	for _, tt := range tests {
		platform, groupUser := parseSessionKey(tt.key)
		if platform != tt.wantPlatform {
			t.Errorf("parseSessionKey(%q) platform = %q, want %q", tt.key, platform, tt.wantPlatform)
		}
		if groupUser != tt.wantGroup {
			t.Errorf("parseSessionKey(%q) groupUser = %q, want %q", tt.key, groupUser, tt.wantGroup)
		}
	}
}

func TestLoadAllSessions(t *testing.T) {
	tmpDir := t.TempDir()
	sessionsDir := filepath.Join(tmpDir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create two session files
	now := time.Now()
	older := now.Add(-24 * time.Hour)

	file1 := sessionFileData{
		Sessions: map[string]*sessionData{
			"s1": {
				ID:   "s1",
				Name: "default",
				History: []core.HistoryEntry{
					{Role: "user", Content: "hello", Timestamp: older},
					{Role: "assistant", Content: "hi", Timestamp: older.Add(time.Minute)},
				},
				CreatedAt: older,
				UpdatedAt: older.Add(time.Minute),
			},
		},
		UserSessions: map[string][]string{
			"feishu:oc_test:ou_user1": {"s1"},
		},
		UserMeta: map[string]*userMetaData{
			"feishu:oc_test:ou_user1": {UserName: "Alice", ChatName: "Test Group"},
		},
	}

	file2 := sessionFileData{
		Sessions: map[string]*sessionData{
			"s1": {
				ID:   "s1",
				Name: "chat",
				History: []core.HistoryEntry{
					{Role: "user", Content: "test", Timestamp: now},
				},
				CreatedAt: now,
				UpdatedAt: now,
			},
			"s2": {
				ID:        "s2",
				Name:      "empty",
				CreatedAt: older,
				UpdatedAt: older,
			},
		},
		UserSessions: map[string][]string{
			"telegram:123:456": {"s1", "s2"},
		},
	}

	writeSessionFile(t, sessionsDir, "project_a.json", file1)
	writeSessionFile(t, sessionsDir, "project_b.json", file2)

	records, err := loadAllSessions(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	// Should have 3 records total (s1 from file1, s1+s2 from file2)
	if len(records) != 3 {
		t.Fatalf("got %d records, want 3", len(records))
	}

	// Should be sorted by LastActive descending
	for i := 1; i < len(records); i++ {
		if records[i].LastActive.After(records[i-1].LastActive) {
			t.Errorf("records not sorted descending: [%d]=%v > [%d]=%v",
				i, records[i].LastActive, i-1, records[i-1].LastActive)
		}
	}

	// Check first record (most recent = project_b:s1)
	first := records[0]
	if first.GlobalID != "project_b:s1" {
		t.Errorf("first record GlobalID = %q, want %q", first.GlobalID, "project_b:s1")
	}
	if first.Platform != "telegram" {
		t.Errorf("first record Platform = %q, want %q", first.Platform, "telegram")
	}
	if first.GroupUser != "123:456" {
		t.Errorf("first record GroupUser = %q, want %q", first.GroupUser, "123:456")
	}
	if first.Messages != 1 {
		t.Errorf("first record Messages = %d, want 1", first.Messages)
	}

	// Check project_a record
	var projectARecord *sessionRecord
	for i := range records {
		if records[i].GlobalID == "project_a:s1" {
			projectARecord = &records[i]
			break
		}
	}
	if projectARecord == nil {
		t.Fatal("project_a:s1 not found")
	}
	if projectARecord.Platform != "feishu" {
		t.Errorf("project_a Platform = %q, want %q", projectARecord.Platform, "feishu")
	}
	if projectARecord.Messages != 2 {
		t.Errorf("project_a Messages = %d, want 2", projectARecord.Messages)
	}
	if projectARecord.UserName != "Alice" {
		t.Errorf("project_a UserName = %q, want %q", projectARecord.UserName, "Alice")
	}
	if projectARecord.ChatName != "Test Group" {
		t.Errorf("project_a ChatName = %q, want %q", projectARecord.ChatName, "Test Group")
	}

	// Check empty session (project_b:s2)
	var emptyRecord *sessionRecord
	for i := range records {
		if records[i].GlobalID == "project_b:s2" {
			emptyRecord = &records[i]
			break
		}
	}
	if emptyRecord == nil {
		t.Fatal("project_b:s2 not found")
	}
	if emptyRecord.Messages != 0 {
		t.Errorf("empty session Messages = %d, want 0", emptyRecord.Messages)
	}
}

func TestLoadAllSessionsSkipsMalformed(t *testing.T) {
	tmpDir := t.TempDir()
	sessionsDir := filepath.Join(tmpDir, "sessions")
	os.MkdirAll(sessionsDir, 0o755)

	// Write one valid file
	valid := sessionFileData{
		Sessions: map[string]*sessionData{
			"s1": {ID: "s1", Name: "ok", UpdatedAt: time.Now()},
		},
		UserSessions: map[string][]string{
			"slack:chan1": {"s1"},
		},
	}
	writeSessionFile(t, sessionsDir, "valid.json", valid)

	// Write one malformed file
	os.WriteFile(filepath.Join(sessionsDir, "bad.json"), []byte("{invalid json"), 0o644)

	records, err := loadAllSessions(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	// Should still load the valid one
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	if records[0].GlobalID != "valid:s1" {
		t.Errorf("record GlobalID = %q, want %q", records[0].GlobalID, "valid:s1")
	}
}

func TestLoadAllSessionsEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	sessionsDir := filepath.Join(tmpDir, "sessions")
	os.MkdirAll(sessionsDir, 0o755)

	records, err := loadAllSessions(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("got %d records, want 0", len(records))
	}
}

func TestLoadAllSessionsNoDir(t *testing.T) {
	tmpDir := t.TempDir()
	// Don't create sessions/ subdirectory

	records, err := loadAllSessions(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if records != nil {
		t.Fatalf("got %v, want nil", records)
	}
}

func writeSessionFile(t *testing.T, dir, name string, data sessionFileData) {
	t.Helper()
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestExtractProjectFromFilename pins the bug where loadAllSessions
// used to derive the project name by stripping only ".json", leaving
// the per-work_dir hash suffix in place. That caused
// `cc-connect sessions list` to display "myproject_a1b2c3d4" and
// broke `cc-connect sessions show myproject:<sid>` lookups.
func TestExtractProjectFromFilename(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "myproject.json", "myproject"},
		{"single-workdir hash", "myproject_a1b2c3d4.json", "myproject"},
		{"multi-workspace hash", "myproject_ws_a1b2c3d4.json", "myproject"},
		{"legacy", "myproject.sessions.json", "myproject"},
		// project names that contain underscores but no 8-hex tail
		// must be preserved verbatim (regression: previous heuristic
		// considered any-length hex; now requires exactly 8 hex chars).
		{"short hex suffix is part of name", "project_a.json", "project_a"},
		{"4-hex suffix is part of name", "foo_dead.json", "foo_dead"},
		{"non-hex suffix is part of name", "myproject_extra.json", "myproject_extra"},
		// real project name happens to end with `_ws_<hex>` pattern
		{"project with snake_case + hash", "my_project_a1b2c3d4.json", "my_project"},
		{"project with _ws_ + hash", "my_project_ws_a1b2c3d4.json", "my_project"},
		// no underscore, no .sessions
		{"single token", "project.json", "project"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractProjectFromFilename(tt.in); got != tt.want {
				t.Fatalf("extractProjectFromFilename(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestLoadAllSessions_StripsWorkDirHash exercises loadAllSessions
// end-to-end against a session file written with the real engine
// naming convention (myproject_<8hex>.json) and confirms the project
// name shown to the user no longer carries the hash suffix.
func TestLoadAllSessions_StripsWorkDirHash(t *testing.T) {
	tmpDir := t.TempDir()
	sessionsDir := filepath.Join(tmpDir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	file := sessionFileData{
		Sessions: map[string]*sessionData{
			"s1": {ID: "s1", Name: "default", UpdatedAt: now},
		},
		UserSessions: map[string][]string{
			"slack:C123": {"s1"},
		},
	}
	// The 8-hex suffix is what sessionStorePath in main.go produces.
	writeSessionFile(t, sessionsDir, "myproject_deadbeef.json", file)

	records, err := loadAllSessions(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	if records[0].Project != "myproject" {
		t.Errorf("Project = %q, want %q", records[0].Project, "myproject")
	}
	if records[0].GlobalID != "myproject:s1" {
		t.Errorf("GlobalID = %q, want %q", records[0].GlobalID, "myproject:s1")
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		in     string
		maxLen int
		want   string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello world", 8, "hello..."},
		{"hello", 3, "hel"},
		{"hello", 1, "h"},
		{"hello", 0, ""},
		{"hello", -1, ""},
		{"日本語テスト", 4, "日..."},
		{"日本語テスト", 3, "日本語"},
		{"日本語テスト", 6, "日本語テスト"},
		{"ab", 4, "ab"},
		{"", 5, ""},
	}
	for _, tt := range tests {
		got := truncate(tt.in, tt.maxLen)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.in, tt.maxLen, got, tt.want)
		}
	}
}
