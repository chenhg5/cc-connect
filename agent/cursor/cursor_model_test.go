package cursor

import (
	"context"
	"os/exec"
	"testing"
)

func TestFetchModelsFromAgentCLI(t *testing.T) {
	// Skip if agent CLI not available
	if _, err := exec.LookPath("agent"); err != nil {
		t.Skip("agent CLI not in PATH")
	}

	ctx := context.Background()
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
	ctx := context.Background()
	models := fetchModelsFromAgentCLI(ctx, "nonexistent-agent-xyz", nil)
	if len(models) != 0 {
		t.Errorf("expected empty when command fails, got %d models", len(models))
	}
}

func TestAvailableModels_Fallback(t *testing.T) {
	// When agent models fails, should fall back to hardcoded list
	ctx := context.Background()
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
	if _, err := exec.LookPath("agent"); err != nil {
		t.Skip("agent CLI not in PATH")
	}

	ctx := context.Background()
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
