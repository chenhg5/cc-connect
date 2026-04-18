package claudecode

import (
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestAgentUsageProbeEnv_AddsHostManagedFlagForCustomProvider(t *testing.T) {
	a := &Agent{
		providers: []core.ProviderConfig{
			{
				Name:    "custom",
				BaseURL: "https://example.com/v1",
				APIKey:  "secret",
			},
		},
		activeIdx: 0,
	}

	env := envSliceToMap(a.usageProbeEnv())

	if got := env["ANTHROPIC_BASE_URL"]; got != "https://example.com/v1" {
		t.Fatalf("ANTHROPIC_BASE_URL = %q, want custom base URL", got)
	}
	if got := env["ANTHROPIC_AUTH_TOKEN"]; got != "secret" {
		t.Fatalf("ANTHROPIC_AUTH_TOKEN = %q, want injected bearer token", got)
	}
	if got := env["CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST"]; got != "1" {
		t.Fatalf("CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST = %q, want 1", got)
	}
}

func TestAgentUsageProbeEnv_DoesNotAddHostManagedFlagForModelOnlyProvider(t *testing.T) {
	a := &Agent{
		providers: []core.ProviderConfig{
			{
				Name:  "model-only",
				Model: "claude-sonnet-4",
			},
		},
		activeIdx: 0,
	}

	env := envSliceToMap(a.usageProbeEnv())
	if _, ok := env["CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST"]; ok {
		t.Fatalf("CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST unexpectedly set: %v", env)
	}
}

func TestAgentUsageProbeEnv_AddsHostManagedFlagForProviderEnvRoutingOverrides(t *testing.T) {
	a := &Agent{
		providers: []core.ProviderConfig{
			{
				Name: "bedrock",
				Env: map[string]string{
					"CLAUDE_CODE_USE_BEDROCK": "1",
				},
			},
		},
		activeIdx: 0,
	}

	env := envSliceToMap(a.usageProbeEnv())
	if got := env["CLAUDE_CODE_USE_BEDROCK"]; got != "1" {
		t.Fatalf("CLAUDE_CODE_USE_BEDROCK = %q, want 1", got)
	}
	if got := env["CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST"]; got != "1" {
		t.Fatalf("CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST = %q, want 1", got)
	}
}

func TestAgentUsageProbeEnv_AddsHostManagedFlagForSessionEnvRoutingOverrides(t *testing.T) {
	a := &Agent{
		sessionEnv: []string{
			"ANTHROPIC_BASE_URL=https://session.example/v1",
		},
	}

	env := envSliceToMap(a.usageProbeEnv())
	if got := env["ANTHROPIC_BASE_URL"]; got != "https://session.example/v1" {
		t.Fatalf("ANTHROPIC_BASE_URL = %q, want session override", got)
	}
	if got := env["CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST"]; got != "1" {
		t.Fatalf("CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST = %q, want 1", got)
	}
}

func TestAgentUsageProbeEnv_AddsHostManagedFlagForRouterOverrides(t *testing.T) {
	a := &Agent{
		routerURL:    "http://127.0.0.1:3456",
		routerAPIKey: "router-secret",
	}

	env := envSliceToMap(a.usageProbeEnv())
	if got := env["ANTHROPIC_BASE_URL"]; got != "http://127.0.0.1:3456" {
		t.Fatalf("ANTHROPIC_BASE_URL = %q, want router URL", got)
	}
	if got := env["ANTHROPIC_API_KEY"]; got != "router-secret" {
		t.Fatalf("ANTHROPIC_API_KEY = %q, want router API key", got)
	}
	if got := env["CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST"]; got != "1" {
		t.Fatalf("CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST = %q, want 1", got)
	}
}

func TestProviderEnv_SetsAnthropicModel(t *testing.T) {
	a := &Agent{
		providers: []core.ProviderConfig{
			{
				Name:    "provider-a",
				BaseURL: "https://a.example.com/v1",
				APIKey:  "key-a",
				Model:   "model-a",
			},
			{
				Name:    "provider-b",
				BaseURL: "https://b.example.com/v1",
				APIKey:  "key-b",
				Model:   "model-b",
			},
		},
		activeIdx: 0,
	}

	env := envSliceToMap(a.providerEnvLocked())
	if got := env["ANTHROPIC_MODEL"]; got != "model-a" {
		t.Fatalf("ANTHROPIC_MODEL = %q, want %q", got, "model-a")
	}
	if got := env["ANTHROPIC_BASE_URL"]; got != "https://a.example.com/v1" {
		t.Fatalf("ANTHROPIC_BASE_URL = %q, want provider-a URL", got)
	}

	a.SetActiveProvider("provider-b")
	env = envSliceToMap(a.providerEnvLocked())
	if got := env["ANTHROPIC_MODEL"]; got != "model-b" {
		t.Fatalf("after switch: ANTHROPIC_MODEL = %q, want %q", got, "model-b")
	}
	if got := env["ANTHROPIC_BASE_URL"]; got != "https://b.example.com/v1" {
		t.Fatalf("after switch: ANTHROPIC_BASE_URL = %q, want provider-b URL", got)
	}
}

func TestProviderEnv_NoModelWhenEmpty(t *testing.T) {
	a := &Agent{
		providers: []core.ProviderConfig{
			{
				Name:    "no-model",
				BaseURL: "https://example.com/v1",
				APIKey:  "key",
			},
		},
		activeIdx: 0,
	}
	env := envSliceToMap(a.providerEnvLocked())
	if _, ok := env["ANTHROPIC_MODEL"]; ok {
		t.Fatalf("ANTHROPIC_MODEL should not be set when provider has no model")
	}
}

func TestProviderEnv_ClearReturnsNil(t *testing.T) {
	a := &Agent{
		providers: []core.ProviderConfig{
			{Name: "p", BaseURL: "https://x.com", APIKey: "k", Model: "m"},
		},
		activeIdx: 0,
	}
	a.SetActiveProvider("")
	env := a.providerEnvLocked()
	if env != nil {
		t.Fatalf("expected nil env after clearing provider, got %v", env)
	}
}

func TestStartSession_UsesActiveProviderModel(t *testing.T) {
	a := &Agent{
		model: "default-model",
		providers: []core.ProviderConfig{
			{Name: "p1", Model: "provider-model-1"},
			{Name: "p2", Model: "provider-model-2"},
		},
		activeIdx: 0,
	}

	a.mu.Lock()
	activeIdx := a.activeIdx
	model := a.model
	if activeIdx >= 0 && activeIdx < len(a.providers) {
		if m := a.providers[activeIdx].Model; m != "" {
			model = m
		}
	}
	a.mu.Unlock()

	if model != "provider-model-1" {
		t.Fatalf("model = %q, want %q", model, "provider-model-1")
	}

	a.SetActiveProvider("p2")
	a.mu.Lock()
	activeIdx = a.activeIdx
	model = a.model
	if activeIdx >= 0 && activeIdx < len(a.providers) {
		if m := a.providers[activeIdx].Model; m != "" {
			model = m
		}
	}
	a.mu.Unlock()

	if model != "provider-model-2" {
		t.Fatalf("after switch: model = %q, want %q", model, "provider-model-2")
	}
}

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
