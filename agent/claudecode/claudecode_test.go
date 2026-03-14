package claudecode

import (
	"testing"
)

func TestParseUserQuestions_ValidInput(t *testing.T) {
	input := map[string]any{
		"questions": []any{
			map[string]any{
				"question":    "Which database?",
				"header":      "Setup",
				"multiSelect": false,
				"options": []any{
					map[string]any{"label": "PostgreSQL", "description": "Production"},
					map[string]any{"label": "SQLite", "description": "Dev"},
				},
			},
		},
	}
	qs := parseUserQuestions(input)
	if len(qs) != 1 {
		t.Fatalf("expected 1 question, got %d", len(qs))
	}
	q := qs[0]
	if q.Question != "Which database?" {
		t.Errorf("question = %q", q.Question)
	}
	if q.Header != "Setup" {
		t.Errorf("header = %q", q.Header)
	}
	if q.MultiSelect {
		t.Error("expected multiSelect=false")
	}
	if len(q.Options) != 2 {
		t.Fatalf("expected 2 options, got %d", len(q.Options))
	}
	if q.Options[0].Label != "PostgreSQL" {
		t.Errorf("option[0].label = %q", q.Options[0].Label)
	}
	if q.Options[1].Description != "Dev" {
		t.Errorf("option[1].description = %q", q.Options[1].Description)
	}
}

func TestParseUserQuestions_EmptyInput(t *testing.T) {
	qs := parseUserQuestions(map[string]any{})
	if len(qs) != 0 {
		t.Errorf("expected 0 questions, got %d", len(qs))
	}
}

func TestParseUserQuestions_NoQuestionText(t *testing.T) {
	input := map[string]any{
		"questions": []any{
			map[string]any{"header": "Setup"},
		},
	}
	qs := parseUserQuestions(input)
	if len(qs) != 0 {
		t.Errorf("expected 0 questions (no question text), got %d", len(qs))
	}
}

func TestParseUserQuestions_MultiSelect(t *testing.T) {
	input := map[string]any{
		"questions": []any{
			map[string]any{
				"question":    "Select features",
				"multiSelect": true,
				"options": []any{
					map[string]any{"label": "Auth"},
					map[string]any{"label": "Logging"},
				},
			},
		},
	}
	qs := parseUserQuestions(input)
	if len(qs) != 1 {
		t.Fatalf("expected 1 question, got %d", len(qs))
	}
	if !qs[0].MultiSelect {
		t.Error("expected multiSelect=true")
	}
}

func TestNormalizePermissionMode(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// dontAsk aliases
		{"dontAsk", "dontAsk"},
		{"dontask", "dontAsk"},
		{"dont-ask", "dontAsk"},
		{"dont_ask", "dontAsk"},
		// bypassPermissions aliases
		{"bypassPermissions", "bypassPermissions"},
		{"yolo", "bypassPermissions"},
		// acceptEdits aliases
		{"acceptEdits", "acceptEdits"},
		{"edit", "acceptEdits"},
		// plan
		{"plan", "plan"},
		// default fallback
		{"", "default"},
		{"unknown", "default"},
	}
	for _, tt := range tests {
		got := normalizePermissionMode(tt.input)
		if got != tt.want {
			t.Errorf("normalizePermissionMode(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSummarizeInput_AskUserQuestion(t *testing.T) {
	input := map[string]any{
		"questions": []any{
			map[string]any{
				"question": "Which framework?",
				"options": []any{
					map[string]any{"label": "React"},
					map[string]any{"label": "Vue"},
				},
			},
		},
	}
	result := summarizeInput("AskUserQuestion", input)
	if result == "" {
		t.Error("expected non-empty summary for AskUserQuestion")
	}
}
