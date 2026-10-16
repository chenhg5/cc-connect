package acp

import (
	"context"
	"strings"
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

func TestModeAlias_ResolvesBeforeDiscovery(t *testing.T) {
	a, err := New(map[string]any{
		"command":      "true",
		"mode":         "normal",
		"mode_aliases": map[string]any{"normal": "accept-edits", "yolo": "bypass"},
	})
	if err != nil {
		t.Fatal(err)
	}
	agent := a.(*Agent)
	if agent.GetMode() != "accept-edits" {
		t.Fatalf("stored mode = %q", agent.GetMode())
	}
	agent.SetMode("yolo")
	if agent.GetMode() != "bypass" {
		t.Fatalf("SetMode yolo = %q", agent.GetMode())
	}
}

func TestLastReportedModel_DisplayOnly(t *testing.T) {
	a, err := New(map[string]any{"command": "true"})
	if err != nil {
		t.Fatal(err)
	}
	agent := a.(*Agent)
	if agent.GetModel() != "" || agent.LastReportedModel() != "" {
		t.Fatalf("model=%q reported=%q", agent.GetModel(), agent.LastReportedModel())
	}
	agent.reportModelOptions("swe-2-medium", nil)
	if agent.LastReportedModel() != "swe-2-medium" || agent.GetModel() != "swe-2-medium" {
		t.Fatalf("reported display model=%q last=%q", agent.GetModel(), agent.LastReportedModel())
	}
	if _, ok := agent.WorkspaceAgentOptions()["model"]; ok {
		t.Fatal("reported model must not be round-tripped")
	}
	if agent.model != "" {
		t.Fatalf("configured model = %q", agent.model)
	}
	agent.reportModelOptions("", []core.ModelOption{{Name: "other", Desc: "Other"}})
	if agent.LastReportedModel() != "swe-2-medium" {
		t.Fatalf("empty current cleared last reported: %q", agent.LastReportedModel())
	}
	if got := agent.AvailableModels(context.Background()); len(got) != 1 || got[0].Name != "other" {
		t.Fatalf("options = %#v", got)
	}
	agent.SetModel("configured")
	if agent.GetModel() != "configured" {
		t.Fatalf("configured model = %q", agent.GetModel())
	}
	if agent.WorkspaceAgentOptions()["model"] != "configured" {
		t.Fatal("configured model was not snapshotted")
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

// TestProbeSpawn_UsesProbeCommand guards the production bug where probes
// (session/list, history, delete) spawned a slow wrapper `cmd` and timed out.
func TestProbeSpawn_UsesProbeCommand(t *testing.T) {
	a := &Agent{cmd: "true", ProbeCommand: "/nonexistent/acp-probe-bin", ProbeArgs: []string{"acp"}}
	_, _, _, err := a.probeSpawn(context.Background(), t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "/nonexistent/acp-probe-bin") {
		t.Fatalf("probeSpawn err = %v, want it to start ProbeCommand", err)
	}
}
