package codex

import (
	"context"
	"testing"
)

func TestNormalizeReasoningEffort_RejectsMinimal(t *testing.T) {
	if got := normalizeReasoningEffort("minimal"); got != "" {
		t.Fatalf("normalizeReasoningEffort(minimal) = %q, want empty", got)
	}
	if got := normalizeReasoningEffort("min"); got != "" {
		t.Fatalf("normalizeReasoningEffort(min) = %q, want empty", got)
	}
}

func TestAvailableReasoningEfforts_ExcludesMinimal(t *testing.T) {
	agent := &Agent{}
	got := agent.AvailableReasoningEfforts()
	want := []string{"low", "medium", "high", "xhigh"}
	if len(got) != len(want) {
		t.Fatalf("AvailableReasoningEfforts len = %d, want %d, got=%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("AvailableReasoningEfforts[%d] = %q, want %q, got=%v", i, got[i], want[i], got)
		}
	}
}

func TestBuildExecArgs_IncludesReasoningEffort(t *testing.T) {
	cs, err := newCodexSession(context.Background(), "/tmp/project", "o3", "high", "full-auto", "", nil)
	if err != nil {
		t.Fatalf("newCodexSession: %v", err)
	}

	args := cs.buildExecArgs("hello")

	want := []string{
		"exec",
		"--json",
		"--skip-git-repo-check",
		"--full-auto",
		"--model",
		"o3",
		"-c",
		`model_reasoning_effort="high"`,
		"--cd",
		"/tmp/project",
		"hello",
	}
	if len(args) != len(want) {
		t.Fatalf("args len = %d, want %d, args=%v", len(args), len(want), args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args[%d] = %q, want %q, args=%v", i, args[i], want[i], args)
		}
	}
}
