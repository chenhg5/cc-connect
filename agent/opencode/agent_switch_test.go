package opencode

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// TestAgentSwitcher_SetAgentAndGetAgent exercises the mutable agentName
// field with a populated enumeration cache (from AvailableAgents).
func TestAgentSwitcher_SetAgentAndGetAgent(t *testing.T) {
	bin := writeFakeAgentListBin(t, strings.Split(realOpencodeAgentListSample, "\n"), 0)
	a := &Agent{cmd: bin, workDir: t.TempDir()}

	if got := a.GetAgent(); got != "" {
		t.Fatalf("GetAgent() = %q, want empty default", got)
	}

	agents := a.AvailableAgents(context.Background())
	if len(agents) == 0 {
		t.Fatal("AvailableAgents() returned no agents for valid sample")
	}

	if err := a.SetAgent("brainstorm"); err != nil {
		t.Fatalf("SetAgent(brainstorm) error: %v", err)
	}
	if got := a.GetAgent(); got != "brainstorm" {
		t.Fatalf("GetAgent() = %q, want brainstorm", got)
	}

	if err := a.SetAgent("plan"); err != nil {
		t.Fatalf("SetAgent(plan) error: %v", err)
	}
	if got := a.GetAgent(); got != "plan" {
		t.Fatalf("GetAgent() = %q, want plan", got)
	}
}

func TestAgentSwitcher_SetAgent_RejectsSubagent(t *testing.T) {
	bin := writeFakeAgentListBin(t, strings.Split(realOpencodeAgentListSample, "\n"), 0)
	a := &Agent{cmd: bin, workDir: t.TempDir()}
	a.AvailableAgents(context.Background())

	if err := a.SetAgent("explore"); err == nil {
		t.Fatal("SetAgent(explore) error = nil, want rejection of subagent")
	}
	if got := a.GetAgent(); got != "" {
		t.Fatalf("GetAgent() = %q, want unchanged empty after rejected switch", got)
	}
}

func TestAgentSwitcher_SetAgent_RejectsUnknown(t *testing.T) {
	bin := writeFakeAgentListBin(t, strings.Split(realOpencodeAgentListSample, "\n"), 0)
	a := &Agent{cmd: bin, workDir: t.TempDir()}
	a.AvailableAgents(context.Background())

	if err := a.SetAgent("does-not-exist"); err == nil {
		t.Fatal("SetAgent(does-not-exist) error = nil, want rejection of unknown agent")
	}
}

func TestAgentSwitcher_SetAgent_WithoutEnumerationOnlyRejectsInternal(t *testing.T) {
	a := &Agent{cmd: "opencode", workDir: t.TempDir()}

	if err := a.SetAgent("compaction"); err == nil {
		t.Fatal("SetAgent(compaction) error = nil, want rejection of internal agent")
	}
	if err := a.SetAgent("brainstorm"); err != nil {
		t.Fatalf("SetAgent(brainstorm) without enumeration error: %v", err)
	}
	if err := a.SetAgent(""); err != nil {
		t.Fatalf("SetAgent(\"\") error: %v, want clear-to-default to succeed", err)
	}
	if got := a.GetAgent(); got != "" {
		t.Fatalf("GetAgent() = %q, want empty after clear", got)
	}
}

func TestAgentSwitcher_SetAgent_ClearRestoresDefault(t *testing.T) {
	bin := writeFakeAgentListBin(t, strings.Split(realOpencodeAgentListSample, "\n"), 0)
	a := &Agent{cmd: bin, workDir: t.TempDir()}
	a.AvailableAgents(context.Background())

	if err := a.SetAgent("brainstorm"); err != nil {
		t.Fatalf("SetAgent(brainstorm) error: %v", err)
	}
	if err := a.SetAgent(""); err != nil {
		t.Fatalf("SetAgent(\"\") error: %v, want clear to succeed", err)
	}
	if got := a.GetAgent(); got != "" {
		t.Fatalf("GetAgent() = %q, want empty default", got)
	}
}

func TestAgentSwitcher_AvailableAgents_FiltersSubagents(t *testing.T) {
	bin := writeFakeAgentListBin(t, strings.Split(realOpencodeAgentListSample, "\n"), 0)
	a := &Agent{cmd: bin, workDir: t.TempDir()}

	agents := a.AvailableAgents(context.Background())
	want := []core.AgentInfo{
		{Name: "build", Mode: "primary"},
		{Name: "plan", Mode: "primary"},
		{Name: "brainstorm", Mode: "all"},
	}
	if !reflect.DeepEqual(agents, want) {
		t.Fatalf("AvailableAgents() = %+v, want %+v", agents, want)
	}
}

func TestAgentSwitcher_AvailableAgents_FailureReturnsNil(t *testing.T) {
	bin := writeFakeAgentListBin(t, nil, 1)
	a := &Agent{cmd: bin, workDir: t.TempDir()}

	if got := a.AvailableAgents(context.Background()); got != nil {
		t.Fatalf("AvailableAgents() = %+v, want nil on enumeration failure", got)
	}

	// After a failed enumeration the cache stays empty, so SetAgent falls
	// back to lenient validation (only internal names rejected).
	if err := a.SetAgent("brainstorm"); err != nil {
		t.Fatalf("SetAgent(brainstorm) after failed enumeration error: %v", err)
	}
}
