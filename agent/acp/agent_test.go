package acp

import (
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

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

	if got, _ := opts["command"].(string); got != "true" {
		t.Fatalf("command = %q, want true", got)
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
