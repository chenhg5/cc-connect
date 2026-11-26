package acp

import "testing"

func TestReasoningEffortFromThoughtLevel(t *testing.T) {
	a := &Agent{}
	a.reportThoughtLevels("medium", []string{"high", "enabled"})

	if got := a.GetReasoningEffort(); got != "medium" {
		t.Fatalf("GetReasoningEffort = %q, want medium", got)
	}
	levels := a.AvailableReasoningEfforts()
	if len(levels) != 2 || levels[0] != "high" || levels[1] != "enabled" {
		t.Fatalf("AvailableReasoningEfforts = %v, want [high enabled]", levels)
	}

	// An explicit /reasoning switch must survive a later server report,
	// the same way modelOverride protects /model across session reloads.
	a.SetReasoningEffort("enabled")
	a.reportThoughtLevels("medium", []string{"high", "enabled"})
	if got := a.GetReasoningEffort(); got != "enabled" {
		t.Fatalf("override lost after server report: %q", got)
	}
	if got := a.thoughtLevelOverride; got != "enabled" {
		t.Fatalf("thoughtLevelOverride = %q, want enabled", got)
	}
}

func TestReasoningEffortUnsupported(t *testing.T) {
	a := &Agent{}
	if levels := a.AvailableReasoningEfforts(); len(levels) != 0 {
		t.Fatalf("AvailableReasoningEfforts = %v, want empty for agents without thought_level", levels)
	}
	if got := a.GetReasoningEffort(); got != "" {
		t.Fatalf("GetReasoningEffort = %q, want empty", got)
	}
}

func TestReasoningEffortInitialFromConfig(t *testing.T) {
	a := &Agent{thoughtLevelOverride: "max"}
	if got := a.thoughtLevelOverride; got != "max" {
		t.Fatalf("reasoning_effort not stored: %q", got)
	}
}
