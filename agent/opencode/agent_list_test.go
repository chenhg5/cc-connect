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
