package acp

import (
	"context"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestAgent_ModelSwitcherContracts(t *testing.T) {
	var _ core.ModelSwitcher = (*Agent)(nil)
}

func TestAgent_AvailableModels_DynamicTakesPriority(t *testing.T) {
	a := &Agent{}

	// Before any handshake there is nothing server-reported; empty is the
	// honest answer and lets callers fall back to their own defaults.
	if initial := a.AvailableModels(context.Background()); len(initial) != 0 {
		t.Fatalf("want empty pre-handshake, got %+v", initial)
	}

	// Session reports dynamic configOptions from server
	a.reportModels("kimi-code/k3", []core.ModelOption{
		{Name: "kimi-code/k3", Alias: "K3", Desc: "Dynamic K3"},
		{Name: "genai/gemini-3.8-flash", Alias: "Gemini", Desc: "Dynamic Gemini"},
	})

	dynamic := a.AvailableModels(context.Background())
	if len(dynamic) != 2 || dynamic[0].Name != "kimi-code/k3" {
		t.Fatalf("AvailableModels = %+v, want dynamic models", dynamic)
	}

	if got := a.GetModel(); got != "kimi-code/k3" {
		t.Fatalf("GetModel = %q, want kimi-code/k3", got)
	}

	a.SetModel("genai/gemini-3.8-flash")
	if got := a.GetModel(); got != "genai/gemini-3.8-flash" {
		t.Fatalf("GetModel after SetModel = %q, want genai/gemini-3.8-flash", got)
	}
}

// TestAgent_ModelOverrideSurvivesServerReport covers the /model switch flow:
// the engine closes the session on switch, and the next handshake's
// session/load reports the model persisted at creation time. That report
// must not revert the user's override.
func TestAgent_ModelOverrideSurvivesServerReport(t *testing.T) {
	a := &Agent{}

	a.SetModel("kimi-code/k3")

	// Next session handshake: server reports the stale persisted model.
	a.reportModels("genai/gemini-3.8-flash", []core.ModelOption{
		{Name: "genai/gemini-3.8-flash", Alias: "Gemini"},
		{Name: "kimi-code/k3", Alias: "K3"},
	})

	if got := a.GetModel(); got != "kimi-code/k3" {
		t.Fatalf("GetModel after stale server report = %q, want override kimi-code/k3", got)
	}
	if a.modelOverride != "kimi-code/k3" {
		t.Fatalf("modelOverride = %q, want kimi-code/k3", a.modelOverride)
	}
	// The model list itself must still refresh from the server.
	if got := a.AvailableModels(context.Background()); len(got) != 2 {
		t.Fatalf("AvailableModels = %+v, want 2 server models", got)
	}
}
