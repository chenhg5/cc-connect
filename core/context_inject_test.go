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

func TestAddHistoryStripsGroupContext(t *testing.T) {
	s := &Session{}
	s.AddHistory("user", "[Group context (last 3)]\n12:00 Jay: hello\n12:01 dev-pro: hi\n---\nactual message")
	if len(s.History) != 1 {
		t.Fatalf("history len = %d, want 1", len(s.History))
	}
	if s.History[0].Content != "actual message" {
		t.Errorf("history content = %q, want %q", s.History[0].Content, "actual message")
	}

	// Test fallback if there is no user content
	s2 := &Session{}
	s2.AddHistory("user", "[Group context (last 3)]\n12:00 Jay: hello\n12:01 dev-pro: hi")
	if len(s2.History) != 1 {
		t.Fatalf("history len = %d, want 1", len(s2.History))
	}
	if s2.History[0].Content != "" {
		t.Errorf("history content = %q, want empty string", s2.History[0].Content)
	}

	// Test non-group-context messages are untouched
	s3 := &Session{}
	s3.AddHistory("user", "regular user message")
	if s3.History[0].Content != "regular user message" {
		t.Errorf("history content = %q, want %q", s3.History[0].Content, "regular user message")
	}
}

func TestStripGroupContext(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"regular message", "regular message"},
		{"[Group context (last 3)]\n12:00 Jay: hello\n---\nactual content", "actual content"},
		{"[Group context (last 3)]\n12:00 Jay: hello", ""},
		{"[Group context (last 3)]\n12:00 Jay: hello\n---\n", ""},
		{"[Group context (foo)]\n12:00 Jay: hello\n---\nactual content", "[Group context (foo)]\n12:00 Jay: hello\n---\nactual content"},
	}
	for _, tc := range tests {
		got := StripGroupContext(tc.input)
		if got != tc.expected {
			t.Errorf("StripGroupContext(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}


