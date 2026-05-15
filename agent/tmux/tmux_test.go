package tmux

import (
	"testing"
)

func TestExtractNew(t *testing.T) {
	tests := []struct {
		name     string
		baseline string
		current  string
		want     string
	}{
		{
			name:     "no change",
			baseline: "foo\nbar",
			current:  "foo\nbar",
			want:     "",
		},
		{
			name:     "empty baseline",
			baseline: "",
			current:  "hello",
			want:     "hello",
		},
		{
			name:     "content grew (fast path)",
			baseline: "foo\nbar",
			current:  "foo\nbar\nbaz",
			want:     "baz",
		},
		{
			name:     "new line after prompt",
			baseline: "user@host:~$ ",
			current:  "user@host:~$ ls\nfile1\nfile2\nuser@host:~$ ",
			want:     "ls\nfile1\nfile2\nuser@host:~$ ",
		},
		{
			name:     "anchor overlap",
			baseline: "line1\nline2\nline3\nline4\nline5",
			current:  "line3\nline4\nline5\nnew1\nnew2",
			want:     "new1\nnew2",
		},
		{
			name:     "fully scrolled - return all current",
			baseline: "old1\nold2\nold3",
			current:  "new1\nnew2\nnew3",
			want:     "new1\nnew2\nnew3",
		},
		{
			name:     "TUI redrawn - shared frame, response replaces prompt",
			baseline: "╭─ Claude ─╮\n\n>",
			current:  "╭─ Claude ─╮\n\nThe answer is 42.\n\n>",
			want:     "The answer is 42.",
		},
		{
			name:     "TUI redrawn - multi-line response",
			baseline: "header\n\n>",
			current:  "header\n\nLine one.\nLine two.\n\n>",
			want:     "Line one.\nLine two.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractNew(tt.baseline, tt.current)
			if got != tt.want {
				t.Errorf("extractNew() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeCapture(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "strip trailing spaces per line",
			raw:  "hello   \nworld   \n",
			want: "hello\nworld",
		},
		{
			name: "strip ANSI color codes",
			raw:  "\x1b[32mgreen\x1b[0m normal",
			want: "green normal",
		},
		{
			name: "strip OSC sequence",
			raw:  "\x1b]0;title\x07prompt$ ",
			want: "prompt$",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeCapture(tt.raw)
			if got != tt.want {
				t.Errorf("normalizeCapture() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewAgentValidation(t *testing.T) {
	// Missing session name should fail
	_, err := New(map[string]any{})
	if err == nil {
		t.Error("expected error when session is empty")
	}

	// With session name but tmux not in PATH - may fail on systems without tmux,
	// so we just verify the session check happens before the tmux PATH check.
}
