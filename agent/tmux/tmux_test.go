package tmux

import (
	"context"
	"testing"
	"time"
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

// TestResolveTargetUniquePerWorkDir verifies that two workDirs sharing the same
// basename but different parent paths never map to the same tmux window target.
func TestResolveTargetUniquePerWorkDir(t *testing.T) {
	a := &Agent{sessionName: "mywork", pane: "0"}

	target1, win1 := a.resolveTarget("mywork", "0", "/repo/a/app")
	target2, win2 := a.resolveTarget("mywork", "0", "/repo/b/app")

	if target1 == target2 {
		t.Errorf("resolveTarget: collision — /repo/a/app and /repo/b/app both produced %q", target1)
	}
	if win1 == win2 {
		t.Errorf("uniqueWindowName: collision — /repo/a/app and /repo/b/app both produced %q", win1)
	}
}

// TestResolveTargetStable verifies that the same workDir always yields the same target.
func TestResolveTargetStable(t *testing.T) {
	a := &Agent{sessionName: "mywork", pane: "0"}

	t1, w1 := a.resolveTarget("mywork", "0", "/repo/a/app")
	t2, w2 := a.resolveTarget("mywork", "0", "/repo/a/app")

	if t1 != t2 || w1 != w2 {
		t.Errorf("resolveTarget not deterministic: got %q/%q then %q/%q", t1, w1, t2, w2)
	}
}

// TestNewTmuxSessionWorkDir verifies that the workDir is stored in the session so
// that file attachments are saved relative to the workspace, not to ".".
func TestNewTmuxSessionWorkDir(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s, err := newTmuxSession(ctx, "sess:win", "sid1", "", 200*time.Millisecond, false, nil, "/tmp/workspace")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if s.workDir != "/tmp/workspace" {
		t.Errorf("workDir = %q, want /tmp/workspace", s.workDir)
	}
}
