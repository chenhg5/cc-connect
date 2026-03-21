package cursor

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func shortTestContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	timeout := 30 * time.Second
	if deadline, ok := t.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			timeout = 100 * time.Millisecond
		} else if remaining < timeout {
			timeout = remaining
		}
	}
	return context.WithTimeout(context.Background(), timeout)
}

func requireWorkingAgentCLI(t *testing.T) {
	t.Helper()
	if os.Getenv("CI") != "" {
		t.Skip("skipping agent CLI test in CI (no real cursor agent available)")
	}
	if os.Getenv("SKIP_REAL_AGENT_CLI") != "" {
		t.Skip("skipping real agent CLI test (SKIP_REAL_AGENT_CLI is set)")
	}
	if _, err := exec.LookPath("agent"); err != nil {
		t.Skip("agent CLI not in PATH")
	}
	ctx, cancel := shortTestContext(t)
	defer cancel()

	out, err := exec.CommandContext(ctx, "agent", "models").CombinedOutput()
	if err != nil {
		t.Skipf("agent CLI is not runnable in this environment: %v (%s)", err, strings.TrimSpace(string(out)))
	}
}

func TestFetchModelsFromAgentCLI(t *testing.T) {
	ctx, cancel := shortTestContext(t)
	defer cancel()
	requireWorkingAgentCLI(t)

	models := fetchModelsFromAgentCLI(ctx, "agent", nil)
	if len(models) == 0 {
		t.Fatal("expected models from agent models, got none")
	}

	// Verify format: each model has non-empty Name
	for i, m := range models {
		if m.Name == "" {
			t.Errorf("models[%d].Name is empty", i)
		}
	}
	// 运行 go test -v 时可见
	t.Logf("fetched %d models:", len(models))
	for i, m := range models {
		t.Logf("  %2d. %s - %s", i+1, m.Name, m.Desc)
	}
}

func TestFetchModelsFromAgentCLI_FailsGracefully(t *testing.T) {
	ctx, cancel := shortTestContext(t)
	defer cancel()
	models := fetchModelsFromAgentCLI(ctx, "nonexistent-agent-xyz", nil)
	if len(models) != 0 {
		t.Errorf("expected empty when command fails, got %d models", len(models))
	}
}

func TestAvailableModels_Fallback(t *testing.T) {
	// When agent models fails, should fall back to hardcoded list
	ctx, cancel := shortTestContext(t)
	defer cancel()
	a := &Agent{cmd: "nonexistent-cmd-that-will-fail"}
	models := a.AvailableModels(ctx)
	fallback := cursorFallbackModels()
	if len(models) != len(fallback) {
		t.Fatalf("fallback models length = %d, want %d", len(models), len(fallback))
	}
	for i := range models {
		if models[i].Name != fallback[i].Name {
			t.Errorf("models[%d].Name = %q, want %q", i, models[i].Name, fallback[i].Name)
		}
	}
}

func TestAvailableModels_FetchFromAgent(t *testing.T) {
	requireWorkingAgentCLI(t)
	ctx, cancel := shortTestContext(t)
	defer cancel()

	a := &Agent{cmd: "agent"}
	models := a.AvailableModels(ctx)
	if len(models) == 0 {
		t.Fatal("expected models from agent models, got none")
	}

	t.Logf("AvailableModels returned %d models:", len(models))
	for i, m := range models {
		t.Logf("  %2d. %s - %s", i+1, m.Name, m.Desc)
	}

	// Should have real models like gpt-5.3-codex, opus-4.6-thinking, etc.
	hasCodex := false
	for _, m := range models {
		if m.Name == "gpt-5.3-codex" || m.Name == "opus-4.6-thinking" || m.Name == "auto" {
			hasCodex = true
			break
		}
	}
	if !hasCodex {
		t.Logf("models: %v", models)
		t.Log("agent models returned models but none of the expected ones (gpt-5.3-codex, opus-4.6-thinking, auto) - may be OK if CLI output format changed")
	}
}
