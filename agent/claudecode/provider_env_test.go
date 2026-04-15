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
