package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAggregateSeatMessagesFiltersTargetProject(t *testing.T) {
	dir := t.TempDir()
	ts := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)

	writeSnapshot := func(project, sessionID, content string) {
		t.Helper()
		snap := sessionSnapshot{
			Sessions: map[string]*Session{
				sessionID: {
					ID: sessionID,
					History: []HistoryEntry{
						{Role: "user", Content: content, Timestamp: ts},
					},
				},
			},
			UserSessions: map[string][]string{
				"telegram:-100123:9001": {sessionID},
			},
		}
		data, err := json.Marshal(snap)
		if err != nil {
			t.Fatalf("marshal snapshot: %v", err)
		}
		path := filepath.Join(dir, project+"_abc123.json")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("write snapshot: %v", err)
		}
	}

	writeSnapshot("architect-claude", "arch-session", "architect task")
	writeSnapshot("dev-pro", "dev-session", "dev task")

	entries := aggregateSeatMessages(dir, 10, "-100123", "architect-claude")
	if len(entries) != 1 {
		t.Fatalf("entries len = %d, want 1: %#v", len(entries), entries)
	}
	if entries[0].Project != "architect-claude" || entries[0].Content != "architect task" {
		t.Fatalf("entry = %#v, want architect project/content", entries[0])
	}
}
