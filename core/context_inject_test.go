package core

import (
	"testing"
)

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
