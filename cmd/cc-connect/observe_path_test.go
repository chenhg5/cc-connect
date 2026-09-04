package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEncodeClaudeProjectKey(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plain absolute path",
			in:   "/home/leigh/workspace/cc-connect",
			want: "-home-leigh-workspace-cc-connect",
		},
		{
			name: "underscore in directory name",
			in:   "/home/alice/my_project",
			want: "-home-alice-my-project",
		},
		{
			name: "space in directory name",
			in:   "/Users/x/Library/Mobile Documents/foo",
			want: "-Users-x-Library-Mobile-Documents-foo",
		},
		{
			name: "tilde in path component",
			in:   "/Users/x/com~apple~CloudDocs/foo",
			want: "-Users-x-com-apple-CloudDocs-foo",
		},
		{
			name: "non-ASCII directory name",
			in:   "/Users/张三/projects/demo",
			want: "-Users----projects-demo",
		},
		{
			name: "windows-style backslashes",
			in:   `C:\Users\Alice\Project`,
			want: "C--Users-Alice-Project",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := encodeClaudeProjectKey(tt.in); got != tt.want {
				t.Fatalf("encodeClaudeProjectKey(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestResolveClaudeProjectDir_HandlesUnderscoreWorkdir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// macOS UserHomeDir falls back to $HOME when set, so this is enough on
	// the platforms the test runs on (Linux/macOS).

	workDir := "/Users/alice/my_project"
	// Claude Code encodes the underscore as a hyphen.
	want := filepath.Join(home, ".claude", "projects", "-Users-alice-my-project")
	if err := os.MkdirAll(want, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got := resolveClaudeProjectDir(workDir)
	if got != want {
		t.Fatalf("resolveClaudeProjectDir(%q) = %q, want %q", workDir, got, want)
	}
}

func TestResolveClaudeProjectDir_HandlesSpaceWorkdir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workDir := "/Users/x/Library/Mobile Documents/com~apple~CloudDocs/foo"
	// Spaces and tildes are encoded as hyphens.
	want := filepath.Join(home, ".claude", "projects", "-Users-x-Library-Mobile-Documents-com-apple-CloudDocs-foo")
	if err := os.MkdirAll(want, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got := resolveClaudeProjectDir(workDir)
	if got != want {
		t.Fatalf("resolveClaudeProjectDir(%q) = %q, want %q", workDir, got, want)
	}
}

func TestResolveClaudeProjectDir_ReturnsEmptyWhenMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if got := resolveClaudeProjectDir("/no/such/workdir"); got != "" {
		t.Fatalf("resolveClaudeProjectDir(missing) = %q, want empty", got)
	}
}

func TestResolveClaudeProjectDir_FallsBackToLegacyEncoding(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Older deployments may have on-disk project keys produced by the old
	// "replace os.PathSeparator only" encoding; tilde was left intact.
	workDir := "/Users/x/com~apple~CloudDocs/foo"
	legacy := "-Users-x-com~apple~CloudDocs-foo"
	want := filepath.Join(home, ".claude", "projects", legacy)
	if err := os.MkdirAll(want, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got := resolveClaudeProjectDir(workDir)
	if got != want {
		t.Fatalf("resolveClaudeProjectDir(%q) = %q, want %q", workDir, got, want)
	}
}
