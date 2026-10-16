package cursor

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeFakeBin creates an executable stub named after a Cursor Agent CLI
// candidate so discovery tests do not depend on a real installation.
func writeFakeBin(t *testing.T, dir, name string) {
	t.Helper()
	file := name
	if runtime.GOOS == "windows" {
		file += ".exe"
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
}

func TestResolveCursorBinary_PrefersCanonicalName(t *testing.T) {
	dir := t.TempDir()
	writeFakeBin(t, dir, "cursor-agent")
	writeFakeBin(t, dir, "agent")
	t.Setenv("PATH", dir)

	got, err := resolveCursorBinary()
	if err != nil {
		t.Fatalf("resolveCursorBinary: %v", err)
	}
	if got != "cursor-agent" {
		t.Errorf("resolveCursorBinary() = %q, want %q", got, "cursor-agent")
	}
}

func TestResolveCursorBinary_FallsBackToAgentAlias(t *testing.T) {
	dir := t.TempDir()
	writeFakeBin(t, dir, "agent")
	t.Setenv("PATH", dir)

	got, err := resolveCursorBinary()
	if err != nil {
		t.Fatalf("resolveCursorBinary: %v", err)
	}
	if got != "agent" {
		t.Errorf("resolveCursorBinary() = %q, want %q", got, "agent")
	}
}

func TestResolveCursorBinary_NoneInstalled(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	got, err := resolveCursorBinary()
	if err == nil {
		t.Fatalf("resolveCursorBinary() = %q, want error when nothing is installed", got)
	}
	// The error must tell the user how to fix it, not just that it failed.
	if !strings.Contains(err.Error(), "cursor.com/install") {
		t.Errorf("error %q should carry install instructions", err)
	}
}

// An explicit `cmd` must be honoured verbatim: a typo has to fail loudly
// rather than silently fall back to whatever else happens to be on PATH.
func TestNew_ExplicitCmdIsNotSecondGuessed(t *testing.T) {
	dir := t.TempDir()
	writeFakeBin(t, dir, "cursor-agent")
	t.Setenv("PATH", dir)

	_, err := New(map[string]any{"cmd": "cursor-agent-typo"})
	if err == nil {
		t.Fatal("New() accepted a bogus cmd; explicit commands must not be second-guessed")
	}
	if !strings.Contains(err.Error(), "cursor-agent-typo") {
		t.Errorf("error %q should name the configured cmd", err)
	}
}

func TestNew_DiscoversCursorAgentWhenCmdUnset(t *testing.T) {
	dir := t.TempDir()
	writeFakeBin(t, dir, "cursor-agent")
	t.Setenv("PATH", dir)

	a, err := New(map[string]any{"work_dir": dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, ok := a.(*Agent)
	if !ok {
		t.Fatalf("New returned %T, want *Agent", a)
	}
	if name := got.CLIBinaryName(); name != "cursor-agent" {
		t.Errorf("CLIBinaryName() = %q, want %q", name, "cursor-agent")
	}
}

func TestBuildCLIArgs_AlwaysTrustsWorkspace(t *testing.T) {
	args := buildCLIArgs(nil, "default", "", "/work", "hi", "")

	if !contains(args, "--trust") {
		t.Errorf("args %v missing --trust", args)
	}
	if !contains(args, "--print") {
		t.Errorf("args %v missing --print", args)
	}
}

// `cmd = "cursor-agent --trust"` is the documented workaround, so it must not
// produce a duplicated flag now that --trust is added automatically.
func TestBuildCLIArgs_TrustFromCmdIsNotDuplicated(t *testing.T) {
	args := buildCLIArgs([]string{"--trust"}, "default", "", "/work", "hi", "")

	if n := count(args, "--trust"); n != 1 {
		t.Errorf("--trust appears %d times in %v, want exactly 1", n, args)
	}
}

func TestBuildCLIArgs_Modes(t *testing.T) {
	tests := []struct {
		mode string
		want []string
	}{
		{"default", nil},
		{"force", []string{"--force"}},
		{"plan", []string{"--mode", "plan"}},
		{"ask", []string{"--mode", "ask"}},
	}
	for _, tc := range tests {
		t.Run(tc.mode, func(t *testing.T) {
			args := buildCLIArgs(nil, tc.mode, "", "/work", "hi", "")
			for _, want := range tc.want {
				if !contains(args, want) {
					t.Errorf("mode %q: args %v missing %q", tc.mode, args, want)
				}
			}
			if tc.mode == "default" && contains(args, "--force") {
				t.Errorf("default mode must not force-approve: %v", args)
			}
		})
	}
}

func TestBuildCLIArgs_ResumeAndModelAndPromptSeparator(t *testing.T) {
	args := buildCLIArgs(nil, "default", "gpt-5", "/work", "do it", "chat-1")

	if !contains(args, "--resume") || !contains(args, "chat-1") {
		t.Errorf("args %v missing resume pair", args)
	}
	if !contains(args, "--model") || !contains(args, "gpt-5") {
		t.Errorf("args %v missing model pair", args)
	}
	// The prompt must sit after "--" so a prompt starting with a dash is not
	// parsed as a flag.
	sep := indexOf(args, "--")
	if sep < 0 {
		t.Fatalf("args %v missing -- separator", args)
	}
	if sep != len(args)-2 || args[len(args)-1] != "do it" {
		t.Errorf("prompt must be the sole argument after --, got %v", args[sep:])
	}
}

func TestBuildCLIArgs_OmitsResumeOnFreshSession(t *testing.T) {
	args := buildCLIArgs(nil, "default", "", "/work", "hi", "")

	if contains(args, "--resume") {
		t.Errorf("fresh session must not pass --resume: %v", args)
	}
}

func contains(args []string, want string) bool { return count(args, want) > 0 }

func count(args []string, want string) int {
	n := 0
	for _, a := range args {
		if a == want {
			n++
		}
	}
	return n
}

func indexOf(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
}
