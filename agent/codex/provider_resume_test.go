package codex

import (
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func envToMap(env []string) map[string]string {
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

// TestCodex_SessionResume_PreservesActiveProvider is a regression test for the
// multi-provider session resume bug (PR #1356). After a cc-connect process
// restart, calling SetActiveProvider with the name persisted on the session
// must restore providerEnv (OPENAI_BASE_URL / OPENAI_API_KEY / model) so that
// the next --resume spawn does not silently use the wrong provider's base_url.
func TestCodex_SessionResume_PreservesActiveProvider(t *testing.T) {
	providers := []core.ProviderConfig{
		{
			Name:    "default-prov",
			BaseURL: "https://default.example.com/v1",
			APIKey:  "default-key",
			Model:   "gpt-4o",
		},
		{
			Name:    "alt-provider",
			BaseURL: "https://alt.example.com/v1",
			APIKey:  "alt-key",
			Model:   "deepseek-v3",
		},
	}

	// Step 1: simulate the user's `/provider switch alt-provider`.
	a1 := &Agent{providers: providers, activeIdx: -1}
	if !a1.SetActiveProvider("alt-provider") {
		t.Fatal("SetActiveProvider(alt-provider) returned false")
	}
	a1.mu.RLock()
	want := envToMap(a1.providerEnvLocked())
	a1.mu.RUnlock()

	if got := want["OPENAI_API_KEY"]; got != "alt-key" {
		t.Fatalf("baseline OPENAI_API_KEY = %q, want alt-key", got)
	}
	if got := want["OPENAI_BASE_URL"]; got != "https://alt.example.com/v1" {
		t.Fatalf("baseline OPENAI_BASE_URL = %q, want alt base URL", got)
	}

	// Step 2: simulate restart — activeIdx is back to -1.
	a2 := &Agent{providers: providers, activeIdx: -1}
	a2.mu.RLock()
	gotBefore := a2.providerEnvLocked()
	a2.mu.RUnlock()
	if gotBefore != nil {
		t.Fatalf("post-restart pre-restore should have nil providerEnv, got %v", gotBefore)
	}

	// Step 3: engine calls restoreActiveProviderFromSession → SetActiveProvider.
	if !a2.SetActiveProvider("alt-provider") {
		t.Fatal("post-restart SetActiveProvider(alt-provider) returned false")
	}
	a2.mu.RLock()
	got := envToMap(a2.providerEnvLocked())
	a2.mu.RUnlock()

	if got["OPENAI_API_KEY"] != want["OPENAI_API_KEY"] {
		t.Fatalf("post-restart OPENAI_API_KEY = %q, want %q", got["OPENAI_API_KEY"], want["OPENAI_API_KEY"])
	}
	if got["OPENAI_BASE_URL"] != want["OPENAI_BASE_URL"] {
		t.Fatalf("post-restart OPENAI_BASE_URL = %q, want %q", got["OPENAI_BASE_URL"], want["OPENAI_BASE_URL"])
	}
}

// TestCodex_SessionResume_ModelFollowsProvider verifies that after restore,
// the model resolved by the session also comes from the active provider.
func TestCodex_SessionResume_ModelFollowsProvider(t *testing.T) {
	providers := []core.ProviderConfig{
		{Name: "p1", Model: "model-from-p1"},
		{Name: "p2", Model: "model-from-p2"},
	}

	a := &Agent{model: "default-model", providers: providers, activeIdx: -1}

	// Before restore: model should be "default-model"
	a.mu.RLock()
	idx := a.activeIdx
	m := a.model
	if idx >= 0 && idx < len(a.providers) {
		if pm := a.providers[idx].Model; pm != "" {
			m = pm
		}
	}
	a.mu.RUnlock()
	if m != "default-model" {
		t.Fatalf("pre-restore model = %q, want default-model", m)
	}

	// Restore: engine sets active provider to "p2"
	a.SetActiveProvider("p2")

	a.mu.RLock()
	idx = a.activeIdx
	m = a.model
	if idx >= 0 && idx < len(a.providers) {
		if pm := a.providers[idx].Model; pm != "" {
			m = pm
		}
	}
	a.mu.RUnlock()
	if m != "model-from-p2" {
		t.Fatalf("post-restore model = %q, want model-from-p2", m)
	}
}
