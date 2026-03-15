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
