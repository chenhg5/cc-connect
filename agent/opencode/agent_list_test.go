package opencode

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// realOpencodeAgentListSample mirrors the actual `opencode agent list`
// output format of opencode 1.18.15 (verified on the dev machine): an
// agent header line "<name> (<mode>)" followed by an indented permission
// JSON block. compaction/title/summary are internal hidden agents and
// appear with mode primary in the list.
const realOpencodeAgentListSample = `build (primary)
  [
  {
    "permission": "*",
    "action": "allow",
    "pattern": "*"
  }
]
compaction (primary)
  [
  {
    "permission": "title",
    "action": "allow",
    "pattern": "*"
  }
]
explore (subagent)
  [
  {
    "permission": "*",
    "action": "ask",
    "pattern": "*"
  }
]
general (subagent)
  [
  {
    "permission": "bash",
    "action": "ask",
    "pattern": "*"
  }
]
plan (primary)
  [
  {
    "permission": "*",
    "action": "ask",
    "pattern": "*"
  }
]
summary (primary)
  [
  {
    "permission": "title",
    "action": "allow",
    "pattern": "*"
  }
]
title (primary)
  [
  {
    "permission": "title",
    "action": "allow",
    "pattern": "*"
  }
]
brainstorm (all)
  [
  {
    "permission": "*",
    "action": "ask",
    "pattern": "*"
  }
]
`

// writeFakeAgentListBin writes a temporary shell script that acts as a
// fake CLI responding to "agent list" with the given lines. When
// exitCode != 0, the script exits immediately with that code.
func writeFakeAgentListBin(t *testing.T, lines []string, exitCode int) string {
	t.Helper()
	tmpDir := t.TempDir()
	name := filepath.Join(tmpDir, "fake-opencode")

	var body strings.Builder
	body.WriteString("#!/bin/sh\n")
	if exitCode != 0 {
		fmt.Fprintf(&body, "exit %d\n", exitCode)
	} else {
		body.WriteString("if [ \"$1\" = \"agent\" ] && [ \"$2\" = \"list\" ]; then\n")
		for _, line := range lines {
			fmt.Fprintf(&body, "printf '%%s\\n' '%s'\n", line)
		}
		body.WriteString("fi\n")
	}

	if err := os.WriteFile(name, []byte(body.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestParseAgentListOutput_RealOpencode11815Sample(t *testing.T) {
	got := parseAgentListOutput(realOpencodeAgentListSample)
	want := []AgentInfo{
		{Name: "build", Mode: AgentModePrimary},
		{Name: "explore", Mode: AgentModeSubagent},
		{Name: "general", Mode: AgentModeSubagent},
		{Name: "plan", Mode: AgentModePrimary},
		{Name: "brainstorm", Mode: AgentModeAll},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseAgentListOutput() = %+v, want %+v", got, want)
	}
}

func TestParseAgentListOutput_EmptyAndGarbage(t *testing.T) {
	if got := parseAgentListOutput(""); len(got) != 0 {
		t.Fatalf("parseAgentListOutput(empty) = %+v, want none", got)
	}
	if got := parseAgentListOutput("  [\n  {\n  }\n]\nnoise line\n"); len(got) != 0 {
		t.Fatalf("parseAgentListOutput(garbage) = %+v, want none", got)
	}
}

func TestListAgents_ZeroMatchesIsError(t *testing.T) {
	bin := writeFakeAgentListBin(t, strings.Split("  [\n  {\n  }\n]\n", "\n"), 0)
	tmpDir := t.TempDir()
	if _, err := listAgents(context.Background(), bin, tmpDir); err == nil {
		t.Fatal("listAgents() with unparsable output: error = nil, want failure")
	}
}

func TestListAgents_CommandFailureIsError(t *testing.T) {
	bin := writeFakeAgentListBin(t, nil, 1)
	tmpDir := t.TempDir()
	if _, err := listAgents(context.Background(), bin, tmpDir); err == nil {
		t.Fatal("listAgents() with failing command: error = nil, want failure")
	}
}

func TestListAgents_ParsesSampleOutput(t *testing.T) {
	bin := writeFakeAgentListBin(t, strings.Split(realOpencodeAgentListSample, "\n"), 0)
	tmpDir := t.TempDir()
	got, err := listAgents(context.Background(), bin, tmpDir)
	if err != nil {
		t.Fatalf("listAgents() error = %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("listAgents() = %+v, want 5 agents", got)
	}
}

func fakeAgentListBin(t *testing.T, sample string, exitCode int) *Agent {
	t.Helper()
	return &Agent{cmd: writeFakeAgentListBin(t, strings.Split(sample, "\n"), exitCode), workDir: t.TempDir()}
}

func TestValidateConfiguredAgent_ValidPrimary(t *testing.T) {
	a := fakeAgentListBin(t, realOpencodeAgentListSample, 0)
	problem, available := a.ValidateConfiguredAgent("build")
	if problem != "" || available != nil {
		t.Fatalf("ValidateConfiguredAgent(build) = %q, %v; want no problem", problem, available)
	}
}

func TestValidateConfiguredAgent_ValidAll(t *testing.T) {
	a := fakeAgentListBin(t, realOpencodeAgentListSample, 0)
	problem, available := a.ValidateConfiguredAgent("brainstorm")
	if problem != "" || available != nil {
		t.Fatalf("ValidateConfiguredAgent(brainstorm) = %q, %v; want no problem", problem, available)
	}
}

func TestValidateConfiguredAgent_EmptySkipped(t *testing.T) {
	a := fakeAgentListBin(t, realOpencodeAgentListSample, 0)
	for _, configured := range []string{"", "   "} {
		problem, available := a.ValidateConfiguredAgent(configured)
		if problem != "" || available != nil {
			t.Fatalf("ValidateConfiguredAgent(%q) = %q, %v; want skip with no problem", configured, problem, available)
		}
	}
}

func TestValidateConfiguredAgent_SubagentReported(t *testing.T) {
	a := fakeAgentListBin(t, realOpencodeAgentListSample, 0)
	problem, available := a.ValidateConfiguredAgent("explore")
	if problem == "" {
		t.Fatal("ValidateConfiguredAgent(explore) problem is empty, want subagent warning")
	}
	if !strings.Contains(problem, "subagent") {
		t.Fatalf("ValidateConfiguredAgent(explore) problem = %q, want subagent reason", problem)
	}
	want := []string{"brainstorm", "build", "plan"}
	if !reflect.DeepEqual(available, want) {
		t.Fatalf("ValidateConfiguredAgent(explore) available = %v, want %v", available, want)
	}
	for _, name := range available {
		if name == "explore" || name == "general" {
			t.Fatalf("ValidateConfiguredAgent(explore) available contains subagent %q", name)
		}
	}
}

func TestValidateConfiguredAgent_UnknownReported(t *testing.T) {
	a := fakeAgentListBin(t, realOpencodeAgentListSample, 0)
	problem, available := a.ValidateConfiguredAgent("nonexistent-agent")
	if problem == "" {
		t.Fatal("ValidateConfiguredAgent(nonexistent-agent) problem is empty, want not-found warning")
	}
	if !strings.Contains(problem, "does not exist") {
		t.Fatalf("ValidateConfiguredAgent(nonexistent-agent) problem = %q, want not-found reason", problem)
	}
	want := []string{"brainstorm", "build", "plan"}
	if !reflect.DeepEqual(available, want) {
		t.Fatalf("ValidateConfiguredAgent(nonexistent-agent) available = %v, want %v", available, want)
	}
}

func TestValidateConfiguredAgent_HiddenAgentReported(t *testing.T) {
	a := fakeAgentListBin(t, realOpencodeAgentListSample, 0)
	for _, configured := range []string{"compaction", "title", "summary"} {
		problem, _ := a.ValidateConfiguredAgent(configured)
		if problem == "" {
			t.Fatalf("ValidateConfiguredAgent(%q) problem is empty, want warning for hidden internal agent", configured)
		}
	}
}

func TestValidateConfiguredAgent_EnumFailureSkipped(t *testing.T) {
	for _, a := range []*Agent{
		fakeAgentListBin(t, "", 1),
		fakeAgentListBin(t, "  [\n  {\n  }\n]\n", 0),
	} {
		problem, available := a.ValidateConfiguredAgent("explore")
		if problem == "" {
			t.Fatal("ValidateConfiguredAgent() problem is empty, want skipped marker")
		}
		if !strings.HasPrefix(problem, agentValidationSkippedPrefix) {
			t.Fatalf("ValidateConfiguredAgent() problem = %q, want skipped prefix", problem)
		}
		if strings.Contains(problem, "does not exist") || strings.Contains(problem, "subagent") {
			t.Fatalf("ValidateConfiguredAgent() problem = %q, must not claim agent invalid", problem)
		}
		if available != nil {
			t.Fatalf("ValidateConfiguredAgent() available = %v, want nil on skipped validation", available)
		}
	}
}

func TestNew_WithConfiguredAgentStillConstructs(t *testing.T) {
	tmpDir := t.TempDir()
	bin := writeFakeAgentListBin(t, strings.Split(realOpencodeAgentListSample, "\n"), 0)

	for _, configured := range []string{"build", "brainstorm", "explore", "nonexistent-agent", ""} {
		opts := map[string]any{
			"cmd":         bin,
			"work_dir":    tmpDir,
			"cc_data_dir": tmpDir,
			"cc_project":  "demo",
		}
		if configured != "" {
			opts["agent"] = configured
		}
		agent, err := New(opts)
		if err != nil {
			t.Fatalf("New(agent=%q) error = %v, want construction to succeed regardless of validation", configured, err)
		}
		if agent == nil {
			t.Fatalf("New(agent=%q) returned nil agent", configured)
		}
	}
}

// realOpencodeDebugAgentsSample mirrors the actual `opencode debug agents`
// output format of OpenCode v2 (verified on the dev machine): a JSON array
// of agent objects carrying id/name/mode/hidden plus large system-prompt
// fields. compaction/title/summary/goal-verify are hidden internal agents;
// explore/general are subagents.
const realOpencodeDebugAgentsSample = `[
  {"id":"brainstorm","name":"brainstorm","mode":"all","hidden":false,"description":"requirements brainstorming"},
  {"id":"build","name":"Build","mode":"primary","hidden":false,"description":"build agent"},
  {"id":"compaction","name":"Compaction","mode":"primary","hidden":true},
  {"id":"explore","name":"Explore","mode":"subagent","hidden":false,"description":"codebase exploration"},
  {"id":"general","name":"General","mode":"subagent","hidden":false,"description":"general research"},
  {"id":"goal","name":"goal","mode":"primary","hidden":false,"description":"goal agent"},
  {"id":"goal-verify","name":"goal-verify","mode":"subagent","hidden":true,"description":"goal verification"},
  {"id":"plan","name":"Plan","mode":"primary","hidden":false,"description":"plan agent"},
  {"id":"summary","name":"Summary","mode":"primary","hidden":true},
  {"id":"title","name":"Title","mode":"primary","hidden":true}
]`

// writeFakeOpencodeBin writes a temporary shell script acting as a fake
// opencode CLI. debugAgentsJSON is printed for `debug agents` (v2 layout)
// and agentListLines for `agent list` (v1 layout). An empty string or empty
// slice makes the corresponding command exit 1, simulating a CLI version
// that does not support it.
func writeFakeOpencodeBin(t *testing.T, debugAgentsJSON string, agentListLines []string) string {
	t.Helper()
	tmpDir := t.TempDir()
	name := filepath.Join(tmpDir, "fake-opencode")

	var body strings.Builder
	body.WriteString("#!/bin/sh\n")

	body.WriteString("if [ \"$1\" = \"debug\" ] && [ \"$2\" = \"agents\" ]; then\n")
	if debugAgentsJSON == "" {
		body.WriteString("  exit 1\n")
	} else {
		body.WriteString("  cat <<'__EOF__'\n")
		body.WriteString(debugAgentsJSON)
		body.WriteString("\n__EOF__\n  exit 0\n")
	}
	body.WriteString("fi\n")

	body.WriteString("if [ \"$1\" = \"agent\" ] && [ \"$2\" = \"list\" ]; then\n")
	if len(agentListLines) == 0 {
		body.WriteString("  exit 1\n")
	} else {
		for _, line := range agentListLines {
			fmt.Fprintf(&body, "  printf '%%s\\n' '%s'\n", line)
		}
		body.WriteString("  exit 0\n")
	}
	body.WriteString("fi\n")

	body.WriteString("exit 1\n")

	if err := os.WriteFile(name, []byte(body.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestParseDebugAgentsOutput_V2Sample(t *testing.T) {
	got, err := parseDebugAgentsOutput(realOpencodeDebugAgentsSample)
	if err != nil {
		t.Fatalf("parseDebugAgentsOutput() error = %v", err)
	}
	want := []AgentInfo{
		{Name: "brainstorm", Mode: AgentModeAll},
		{Name: "build", Mode: AgentModePrimary},
		{Name: "explore", Mode: AgentModeSubagent},
		{Name: "general", Mode: AgentModeSubagent},
		{Name: "goal", Mode: AgentModePrimary},
		{Name: "plan", Mode: AgentModePrimary},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseDebugAgentsOutput() = %+v, want %+v", got, want)
	}
}

func TestParseDebugAgentsOutput_InvalidJSON(t *testing.T) {
	if _, err := parseDebugAgentsOutput(realOpencodeAgentListSample); err == nil {
		t.Fatal("parseDebugAgentsOutput(v1 text output) error = nil, want failure")
	}
}

func TestParseDebugAgentsOutput_SkipsUnknownModeAndEmptyNames(t *testing.T) {
	got, err := parseDebugAgentsOutput(`[
	  {"id":"","name":"","mode":"primary"},
	  {"id":"weird","name":"Weird","mode":"mystery"},
	  {"id":"ok","name":"OK","mode":"primary"}
	]`)
	if err != nil {
		t.Fatalf("parseDebugAgentsOutput() error = %v", err)
	}
	want := []AgentInfo{{Name: "ok", Mode: AgentModePrimary}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseDebugAgentsOutput() = %+v, want %+v", got, want)
	}
}

func TestListAgents_PrefersV2DebugAgentsJSON(t *testing.T) {
	// Both layouts respond; the v2 JSON must win.
	bin := writeFakeOpencodeBin(t, realOpencodeDebugAgentsSample, strings.Split(realOpencodeAgentListSample, "\n"))
	got, err := listAgents(context.Background(), bin, t.TempDir())
	if err != nil {
		t.Fatalf("listAgents() error = %v", err)
	}
	want := []AgentInfo{
		{Name: "brainstorm", Mode: AgentModeAll},
		{Name: "build", Mode: AgentModePrimary},
		{Name: "explore", Mode: AgentModeSubagent},
		{Name: "general", Mode: AgentModeSubagent},
		{Name: "goal", Mode: AgentModePrimary},
		{Name: "plan", Mode: AgentModePrimary},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("listAgents() = %+v, want v2 JSON result %+v", got, want)
	}
}

func TestListAgents_FallsBackToV1AgentList(t *testing.T) {
	// `debug agents` unsupported (v1 CLI); the text layout must be used.
	bin := writeFakeOpencodeBin(t, "", strings.Split(realOpencodeAgentListSample, "\n"))
	got, err := listAgents(context.Background(), bin, t.TempDir())
	if err != nil {
		t.Fatalf("listAgents() error = %v", err)
	}
	want := []AgentInfo{
		{Name: "build", Mode: AgentModePrimary},
		{Name: "explore", Mode: AgentModeSubagent},
		{Name: "general", Mode: AgentModeSubagent},
		{Name: "plan", Mode: AgentModePrimary},
		{Name: "brainstorm", Mode: AgentModeAll},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("listAgents() = %+v, want v1 fallback result %+v", got, want)
	}
}

func TestAgentSwitcher_V2EnumerationDrivesSetAgent(t *testing.T) {
	bin := writeFakeOpencodeBin(t, realOpencodeDebugAgentsSample, nil)
	a := &Agent{cmd: bin, workDir: t.TempDir()}

	agents := a.AvailableAgents(context.Background())
	want := []core.AgentInfo{
		{Name: "brainstorm", Mode: "all"},
		{Name: "build", Mode: "primary"},
		{Name: "goal", Mode: "primary"},
		{Name: "plan", Mode: "primary"},
	}
	if !reflect.DeepEqual(agents, want) {
		t.Fatalf("AvailableAgents() = %+v, want %+v", agents, want)
	}

	if err := a.SetAgent("goal"); err != nil {
		t.Fatalf("SetAgent(goal) error: %v", err)
	}
	if err := a.SetAgent("goal-verify"); err == nil {
		t.Fatal("SetAgent(goal-verify) error = nil, want rejection of hidden subagent")
	}
	if err := a.SetAgent("explore"); err == nil {
		t.Fatal("SetAgent(explore) error = nil, want rejection of subagent")
	}
}

func TestValidateConfiguredAgent_V2Enumeration(t *testing.T) {
	a := &Agent{cmd: writeFakeOpencodeBin(t, realOpencodeDebugAgentsSample, nil), workDir: t.TempDir()}

	if problem, _ := a.ValidateConfiguredAgent("build"); problem != "" {
		t.Fatalf("ValidateConfiguredAgent(build) problem = %q, want none", problem)
	}

	problem, available := a.ValidateConfiguredAgent("compaction")
	if problem == "" {
		t.Fatal("ValidateConfiguredAgent(compaction) problem is empty, want hidden-agent warning")
	}
	want := []string{"brainstorm", "build", "goal", "plan"}
	if !reflect.DeepEqual(available, want) {
		t.Fatalf("ValidateConfiguredAgent(compaction) available = %v, want %v", available, want)
	}
}
