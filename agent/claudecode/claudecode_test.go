package claudecode

import (
	"strings"
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

func TestSummarizeInput_Read(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]any
		expected string
	}{
		{"file_path", map[string]any{"file_path": "README.md"}, "README.md"},
		{"path", map[string]any{"path": "src/main.go"}, "src/main.go"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summarizeInput("Read", tt.input)
			if got != tt.expected {
				t.Errorf("summarizeInput() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestSummarizeInput_Write(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]any
		contains []string // strings that should be in output
	}{
		{
			name: "with content",
			input: map[string]any{
				"file_path": "test.txt",
				"content":  "line1\nline2\nline3",
			},
			contains: []string{"`test.txt`", "```", "line1", "line2", "line3"},
		},
		{
			name: "long content",
			input: map[string]any{
				"file_path": "test.txt",
				"content": strings.Repeat("line\n", 20),
			},
			contains: []string{"`test.txt`", "```", "line"}, // no longer truncates, sends full content
		},
		{
			name:     "path variant",
			input:    map[string]any{"path": "README.md", "content": "hello"},
			contains: []string{"`README.md`", "```", "hello"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summarizeInput("Write", tt.input)
			for _, substr := range tt.contains {
				if !strings.Contains(got, substr) {
					t.Errorf("summarizeInput() = %q, should contain %q", got, substr)
				}
			}
		})
	}
}

func TestSummarizeInput_Edit(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]any
		contains []string // strings that should be in output
	}{
		{
			name: "with diff",
			input: map[string]any{
				"file_path": "test.txt",
				"old_str":   "old line",
				"new_str":   "new line",
			},
			contains: []string{"`test.txt`", "```diff", "- old line", "+ new line"},
		},
		{
			name: "old_string variant",
			input: map[string]any{
				"path":        "file.txt",
				"old_string":  "foo",
				"new_string":  "bar",
			},
			contains: []string{"`file.txt`", "```diff", "- foo", "+ bar"},
		},
		{
			name:     "no changes",
			input:    map[string]any{"file_path": "test.txt", "old_str": "", "new_str": ""},
			contains: []string{"test.txt"}, // no diff, just filename
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summarizeInput("Edit", tt.input)
			for _, substr := range tt.contains {
				if !strings.Contains(got, substr) {
					t.Errorf("summarizeInput() = %q, should contain %q", got, substr)
				}
			}
		})
	}
}

func TestSummarizeInput_Bash(t *testing.T) {
	input := map[string]any{"command": "ls -la"}
	got := summarizeInput("Bash", input)
	if got != "ls -la" {
		t.Errorf("summarizeInput() = %q, want %q", got, "ls -la")
	}
}

func TestSummarizeInput_Grep(t *testing.T) {
	input := map[string]any{"pattern": "TODO"}
	got := summarizeInput("Grep", input)
	if got != "TODO" {
		t.Errorf("summarizeInput() = %q, want %q", got, "TODO")
	}
}

func TestSummarizeInput_Glob(t *testing.T) {
	tests := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{"pattern", map[string]any{"pattern": "*.go"}, "*.go"},
		{"glob_pattern", map[string]any{"glob_pattern": "**/*.js"}, "**/*.js"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summarizeInput("Glob", tt.input)
			if got != tt.want {
				t.Errorf("summarizeInput() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestComputeLineDiff(t *testing.T) {
	tests := []struct {
		name string
		old  string
		new  string
		want string
	}{
		{
			name: "simple change",
			old:  "line1\nline2\nline3",
			new:  "line1\nchanged\nline3",
			want: "  line1\n- line2\n+ changed\n  line3",
		},
		{
			name: "no changes",
			old:  "same\ncontent",
			new:  "same\ncontent",
			want: "",
		},
		{
			name: "all different",
			old:  "old1\nold2",
			new:  "new1\nnew2",
			want: "- old1\n- old2\n+ new1\n+ new2",
		},
		{
			name: "addition",
			old:  "line1\nline3",
			new:  "line1\nline2\nline3",
			want: "  line1\n+ line2\n  line3",
		},
		{
			name: "deletion",
			old:  "line1\nline2\nline3",
			new:  "line1\nline3",
			want: "  line1\n- line2\n  line3",
		},
		{
			name: "multline with context",
			old:  "a\nb\nc\nd\ne\nf",
			new:  "a\nb\nX\nd\ne\nY",
			want: "  ...\n  b\n- c\n- d\n- e\n- f\n+ X\n+ d\n+ e\n+ Y",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeLineDiff(tt.old, tt.new)
			if got != tt.want {
				t.Errorf("computeLineDiff() =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}
