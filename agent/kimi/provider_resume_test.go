package kimi

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

// TestKimi_SessionResume_PreservesActiveProvider is a regression test for the
// multi-provider session resume bug (PR #1356). After a cc-connect process
// restart, calling SetActiveProvider with the name persisted on the session
// must restore providerEnv (KIMI_API_KEY + custom env) so that the next
// --resume spawn does not silently use the wrong provider.
func TestKimi_SessionResume_PreservesActiveProvider(t *testing.T) {
	providers := []core.ProviderConfig{
		{
			Name:   "default-prov",
			APIKey: "default-key",
			Model:  "kimi-default",
		},
		{
			Name:   "alt-provider",
			APIKey: "alt-key",
			Model:  "kimi-alt",
			Env:    map[string]string{"CUSTOM_FLAG": "1"},
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

	if got := want["KIMI_API_KEY"]; got != "alt-key" {
		t.Fatalf("baseline KIMI_API_KEY = %q, want alt-key", got)
	}
	if got := want["CUSTOM_FLAG"]; got != "1" {
		t.Fatalf("baseline CUSTOM_FLAG = %q, want 1", got)
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

	if got["KIMI_API_KEY"] != want["KIMI_API_KEY"] {
		t.Fatalf("post-restart KIMI_API_KEY = %q, want %q", got["KIMI_API_KEY"], want["KIMI_API_KEY"])
	}
	if got["CUSTOM_FLAG"] != want["CUSTOM_FLAG"] {
		t.Fatalf("post-restart CUSTOM_FLAG = %q, want %q", got["CUSTOM_FLAG"], want["CUSTOM_FLAG"])
	}
}

// TestKimi_SessionResume_ModelFollowsProvider verifies that after restore,
// the model resolved by the agent also comes from the active provider.
func TestKimi_SessionResume_ModelFollowsProvider(t *testing.T) {
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
