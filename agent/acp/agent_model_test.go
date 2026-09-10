package acp

import (
	"context"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// --- Agent: model cache & ModelSwitcher ---------------------------------

func TestAgent_AvailableModels_emptyBeforeHandshake(t *testing.T) {
	a := &Agent{}
	ctx := context.Background()
	if got := a.AvailableModels(ctx); len(got) != 0 {
		t.Fatalf("want empty models before first handshake, got %v", got)
	}
	if got := a.GetModel(); got != "" {
		t.Fatalf("want empty model before handshake, got %q", got)
	}
}

func TestAgent_reportModelOptions_populatesCache(t *testing.T) {
	a := &Agent{}
	opts := []core.ModelOption{
		{Name: "deepseek/deepseek-v4-pro", Desc: "DeepSeek V4 Pro"},
		{Name: "deepseek/deepseek-v4-flash", Desc: "DeepSeek V4 Flash"},
	}
	a.reportModelOptions(opts, "deepseek/deepseek-v4-pro")

	ctx := context.Background()
	models := a.AvailableModels(ctx)
	if len(models) != 2 {
		t.Fatalf("got %d models, want 2", len(models))
	}
	if models[0].Name != "deepseek/deepseek-v4-pro" || models[0].Desc != "DeepSeek V4 Pro" {
		t.Fatalf("models[0] = %+v", models[0])
	}

	// No explicit SetModel, so GetModel falls back to server-reported currentValue.
	if got := a.GetModel(); got != "deepseek/deepseek-v4-pro" {
		t.Fatalf("GetModel = %q, want deepseek/deepseek-v4-pro (fallback to server currentValue)", got)
	}
}

func TestAgent_SetModel_overridesPending(t *testing.T) {
	a := &Agent{}
	a.reportModelOptions([]core.ModelOption{
		{Name: "deepseek/deepseek-v4-pro", Desc: "Pro"},
	}, "deepseek/deepseek-v4-pro")

	// Explicit SetModel must win over server-reported currentValue.
	a.SetModel("deepseek/deepseek-v4-flash")
	if got := a.GetModel(); got != "deepseek/deepseek-v4-flash" {
		t.Fatalf("GetModel = %q, want deepseek/deepseek-v4-flash (explicit SetModel wins)", got)
	}

	// AvailableModels still returns the cached list regardless of SetModel.
	ctx := context.Background()
	models := a.AvailableModels(ctx)
	if len(models) != 1 {
		t.Fatalf("want 1 model in cache, got %d", len(models))
	}
}

func TestAgent_SetModel_emptyResets(t *testing.T) {
	a := &Agent{}
	a.SetModel("deepseek/deepseek-v4-pro")
	if got := a.GetModel(); got != "deepseek/deepseek-v4-pro" {
		t.Fatalf("GetModel = %q after SetModel", got)
	}
	a.SetModel("")
	if got := a.GetModel(); got != "" {
		t.Fatalf("GetModel = %q after SetModel(\"\"), want empty", got)
	}
}
