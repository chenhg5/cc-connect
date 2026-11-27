package claudecode

import (
	"slices"
	"testing"
)

// Claude Code accepts --effort low|medium|high|xhigh|max. xhigh used to fall
// through normalizeEffort's default branch, so reasoning_effort = "xhigh" and
// /reasoning xhigh were silently dropped and no --effort flag was passed.
func TestNormalizeEffort_AcceptsXhigh(t *testing.T) {
	for _, in := range []string{"xhigh", "XHigh", " xhigh ", "x-high", "extra-high", "extra_high"} {
		if got := normalizeEffort(in); got != "xhigh" {
			t.Errorf("normalizeEffort(%q) = %q, want %q", in, got, "xhigh")
		}
	}
	for in, want := range map[string]string{"low": "low", "med": "medium", "high": "high", "max": "max", "ultra": ""} {
		if got := normalizeEffort(in); got != want {
			t.Errorf("normalizeEffort(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAvailableReasoningEfforts_MatchesCLILevels(t *testing.T) {
	got := (&Agent{}).AvailableReasoningEfforts()
	want := []string{"low", "medium", "high", "xhigh", "max"}
	if !slices.Equal(got, want) {
		t.Fatalf("AvailableReasoningEfforts() = %v, want %v", got, want)
	}
	for _, e := range got {
		if normalizeEffort(e) != e {
			t.Errorf("menu offers %q but normalizeEffort rejects it", e)
		}
	}
}

func TestSetReasoningEffort_KeepsXhigh(t *testing.T) {
	a := &Agent{}
	a.SetReasoningEffort("xhigh")
	if got := a.GetReasoningEffort(); got != "xhigh" {
		t.Fatalf("GetReasoningEffort() = %q after SetReasoningEffort(\"xhigh\"), want \"xhigh\"", got)
	}
}
