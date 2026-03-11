package codex

import (
	"context"
	"testing"
)

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
