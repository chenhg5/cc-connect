package acp

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// fakeTraeCLIOnPath creates a stub "traecli" executable in a temp dir and
// prepends that dir to PATH for the duration of the test. New() only needs the
// command to be resolvable via exec.LookPath (and named "traecli"/"traex") to
// construct a *TraeAgent; the stub is never actually run by these unit tests.
func fakeTraeCLIOnPath(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake CLI stub uses a POSIX shebang; skip on Windows")
	}
	dir := t.TempDir()
	stub := filepath.Join(dir, "traecli")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestNew_DisplayNameDefault(t *testing.T) {
	a, err := New(map[string]any{"command": "true"})
	if err != nil {
		t.Fatal(err)
	}
	agent := a.(*Agent)
	if got := agent.CLIDisplayName(); got != "ACP" {
		t.Fatalf("CLIDisplayName = %q, want ACP", got)
	}
}

func TestNew_DisplayNameCustom(t *testing.T) {
	a, err := New(map[string]any{
		"command":      "true",
		"display_name": "Copilot ACP",
	})
	if err != nil {
		t.Fatal(err)
	}
	agent := a.(*Agent)
	if got := agent.CLIDisplayName(); got != "Copilot ACP" {
		t.Fatalf("CLIDisplayName = %q, want Copilot ACP", got)
	}
}

func TestWorkspaceAgentOptions(t *testing.T) {
	a, err := New(map[string]any{
		"command":      "true",
		"args":         []any{"--acp", "--stdio"},
		"env":          map[string]any{"FOO": "bar", "COPILOT_VALUE": "a=b"},
		"auth_method":  "cursor_login",
		"display_name": "Copilot ACP",
	})
	if err != nil {
		t.Fatal(err)
	}

	agent := a.(*Agent)
	agent.SetSessionEnv([]string{"SESSION_ONLY=1"})

	snapshotter, ok := a.(core.WorkspaceAgentOptionSnapshotter)
	if !ok {
		t.Fatalf("agent does not implement WorkspaceAgentOptionSnapshotter")
	}
	opts := snapshotter.WorkspaceAgentOptions()

	if got, _ := opts["cmd"].(string); got != "true" {
		t.Fatalf("cmd = %q, want true", got)
	}
	gotArgs, _ := opts["args"].([]string)
	if len(gotArgs) != 2 || gotArgs[0] != "--acp" || gotArgs[1] != "--stdio" {
		t.Fatalf("args = %#v, want [--acp --stdio]", gotArgs)
	}
	gotEnv, _ := opts["env"].(map[string]string)
	if len(gotEnv) != 2 || gotEnv["FOO"] != "bar" || gotEnv["COPILOT_VALUE"] != "a=b" {
		t.Fatalf("env = %#v, want config env only", gotEnv)
	}
	if got, _ := opts["auth_method"].(string); got != "cursor_login" {
		t.Fatalf("auth_method = %q, want cursor_login", got)
	}
	if got, _ := opts["display_name"].(string); got != "Copilot ACP" {
		t.Fatalf("display_name = %q, want Copilot ACP", got)
	}
}

func TestNew_TraeCLIImplementsModelSwitcher(t *testing.T) {
	fakeTraeCLIOnPath(t)
	a, err := New(map[string]any{
		"command": "traecli",
		"args":    []any{"acp", "serve"},
		"model":   "test-model-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.(*TraeAgent); !ok {
		t.Fatalf("New(traecli) returned %T, want *TraeAgent", a)
	}
	switcher, ok := a.(core.ModelSwitcher)
	if !ok {
		t.Fatalf("TraeAgent does not implement ModelSwitcher")
	}
	if got := switcher.GetModel(); got != "test-model-a" {
		t.Fatalf("GetModel = %q, want test-model-a", got)
	}
	switcher.SetModel("test-model-b")
	if got := switcher.GetModel(); got != "test-model-b" {
		t.Fatalf("GetModel after SetModel = %q, want test-model-b", got)
	}
	opts := a.(core.WorkspaceAgentOptionSnapshotter).WorkspaceAgentOptions()
	if got, _ := opts["model"].(string); got != "test-model-b" {
		t.Fatalf("snapshot model = %q, want test-model-b", got)
	}
}

func TestNew_GenericACPDoesNotImplementModelSwitcher(t *testing.T) {
	a, err := New(map[string]any{
		"command": "true",
		"args":    []any{"acp", "serve"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.(*Agent); !ok {
		t.Fatalf("New(true) returned %T, want *Agent", a)
	}
	if _, ok := a.(core.ModelSwitcher); ok {
		t.Fatalf("generic ACP agent unexpectedly implements ModelSwitcher")
	}
}
