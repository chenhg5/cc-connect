package opencode

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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
