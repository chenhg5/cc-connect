package opencode

import (
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func envSliceToMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		out[key] = value
	}
	return out
}

// TestOpencode_SessionResume_PreservesActiveProvider is a regression test for
// the multi-provider session resume bug (PR #1356). After a cc-connect process
// restart, calling SetActiveProvider with the name persisted on the session
// must restore providerEnv (ANTHROPIC_API_KEY + custom env) so that the next
// --resume spawn does not silently use the wrong provider.
func TestOpencode_SessionResume_PreservesActiveProvider(t *testing.T) {
	providers := []core.ProviderConfig{
		{
			Name:   "default-prov",
			APIKey: "default-key",
			Model:  "claude-sonnet-4-20250514",
			Env:    map[string]string{"OPENAI_BASE_URL": "https://default.example.com/v1"},
		},
		{
			Name:   "alt-provider",
			APIKey: "alt-key",
			Model:  "deepseek-chat",
			Env:    map[string]string{"OPENAI_BASE_URL": "https://alt.example.com/v1"},
		},
	}

	// Step 1: simulate `/provider switch alt-provider`.
	a1 := &Agent{providers: providers, activeIdx: -1}
	if !a1.SetActiveProvider("alt-provider") {
		t.Fatal("SetActiveProvider(alt-provider) returned false")
	}
	a1.mu.Lock()
	want := envSliceToMap(a1.providerEnvLocked())
	a1.mu.Unlock()

	if got := want["ANTHROPIC_API_KEY"]; got != "alt-key" {
		t.Fatalf("baseline ANTHROPIC_API_KEY = %q, want alt-key", got)
	}
	if got := want["OPENAI_BASE_URL"]; got != "https://alt.example.com/v1" {
		t.Fatalf("baseline OPENAI_BASE_URL = %q, want alt URL", got)
	}

	// Step 2: simulate restart — activeIdx is back to -1.
	a2 := &Agent{providers: providers, activeIdx: -1}
	a2.mu.Lock()
	gotBefore := a2.providerEnvLocked()
	a2.mu.Unlock()
	if gotBefore != nil {
		t.Fatalf("post-restart pre-restore should have nil providerEnv, got %v", gotBefore)
	}

	// Step 3: engine calls restoreActiveProviderFromSession → SetActiveProvider.
	if !a2.SetActiveProvider("alt-provider") {
		t.Fatal("post-restart SetActiveProvider(alt-provider) returned false")
	}
	a2.mu.Lock()
	got := envSliceToMap(a2.providerEnvLocked())
	a2.mu.Unlock()

	if got["ANTHROPIC_API_KEY"] != want["ANTHROPIC_API_KEY"] {
		t.Fatalf("post-restart ANTHROPIC_API_KEY = %q, want %q", got["ANTHROPIC_API_KEY"], want["ANTHROPIC_API_KEY"])
	}
	if got["OPENAI_BASE_URL"] != want["OPENAI_BASE_URL"] {
		t.Fatalf("post-restart OPENAI_BASE_URL = %q, want %q", got["OPENAI_BASE_URL"], want["OPENAI_BASE_URL"])
	}
}

// TestOpencode_SessionResume_ModelFollowsProvider verifies that after restore,
// the model resolved by the agent also comes from the active provider.
func TestOpencode_SessionResume_ModelFollowsProvider(t *testing.T) {
	providers := []core.ProviderConfig{
		{Name: "p1", Model: "model-from-p1"},
		{Name: "p2", Model: "model-from-p2"},
	}

	a := &Agent{model: "default-model", providers: providers, activeIdx: -1}

	// Before restore: model should be "default-model"
	a.mu.Lock()
	idx := a.activeIdx
	m := a.model
	if idx >= 0 && idx < len(a.providers) {
		if pm := a.providers[idx].Model; pm != "" {
			m = pm
		}
	}
	a.mu.Unlock()
	if m != "default-model" {
		t.Fatalf("pre-restore model = %q, want default-model", m)
	}

	// Restore: engine sets active provider to "p2"
	a.SetActiveProvider("p2")

	a.mu.Lock()
	idx = a.activeIdx
	m = a.model
	if idx >= 0 && idx < len(a.providers) {
		if pm := a.providers[idx].Model; pm != "" {
			m = pm
		}
	}
	a.mu.Unlock()
	if m != "model-from-p2" {
		t.Fatalf("post-restore model = %q, want model-from-p2", m)
	}
}
