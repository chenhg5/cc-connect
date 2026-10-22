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

func TestProviderEnv_BedrockThinkingRewrite(t *testing.T) {
	a := &Agent{
		providers: []core.ProviderConfig{
			{
				Name: "bedrock",
				Env: map[string]string{
					"CLAUDE_CODE_USE_BEDROCK": "1",
					"AWS_PROFILE":             "bedrock",
				},
				Thinking: "disabled",
			},
		},
		activeIdx: 0,
	}

	env := envSliceToMap(a.providerEnvLocked())

	// Should set ANTHROPIC_BEDROCK_BASE_URL to local proxy URL.
	baseURL := env["ANTHROPIC_BEDROCK_BASE_URL"]
	if baseURL == "" {
		t.Fatalf("ANTHROPIC_BEDROCK_BASE_URL should be set for Bedrock with thinking rewrite")
	}
	if !strings.HasPrefix(baseURL, "http://127.0.0.1:") {
		t.Fatalf("ANTHROPIC_BEDROCK_BASE_URL = %q, want local proxy URL", baseURL)
	}

	// Should preserve Bedrock env vars.
	if got := env["CLAUDE_CODE_USE_BEDROCK"]; got != "1" {
		t.Fatalf("CLAUDE_CODE_USE_BEDROCK = %q, want 1", got)
	}

	// Should set NO_PROXY for local proxy.
	if got := env["NO_PROXY"]; got != "127.0.0.1" {
		t.Fatalf("NO_PROXY = %q, want 127.0.0.1", got)
	}
}

func TestProviderEnv_VertexThinkingRewrite(t *testing.T) {
	a := &Agent{
		providers: []core.ProviderConfig{
			{
				Name: "vertex",
				Env: map[string]string{
					"CLAUDE_CODE_USE_VERTEX": "1",
					"CLOUD_ML_REGION":        "us-east1",
				},
				Thinking: "disabled",
			},
		},
		activeIdx: 0,
	}

	env := envSliceToMap(a.providerEnvLocked())

	// Should set ANTHROPIC_VERTEX_BASE_URL to local proxy URL.
	baseURL := env["ANTHROPIC_VERTEX_BASE_URL"]
	if baseURL == "" {
		t.Fatalf("ANTHROPIC_VERTEX_BASE_URL should be set for Vertex with thinking rewrite")
	}
	if !strings.HasPrefix(baseURL, "http://127.0.0.1:") {
		t.Fatalf("ANTHROPIC_VERTEX_BASE_URL = %q, want local proxy URL", baseURL)
	}
}

func TestProviderEnv_BedrockNoThinking(t *testing.T) {
	// Without thinking override, Bedrock provider should not use proxy.
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

	env := envSliceToMap(a.providerEnvLocked())

	// Should NOT set ANTHROPIC_BEDROCK_BASE_URL when thinking is not set.
	if _, ok := env["ANTHROPIC_BEDROCK_BASE_URL"]; ok {
		t.Fatalf("ANTHROPIC_BEDROCK_BASE_URL should not be set without thinking override")
	}

	// Should preserve Bedrock env var.
	if got := env["CLAUDE_CODE_USE_BEDROCK"]; got != "1" {
		t.Fatalf("CLAUDE_CODE_USE_BEDROCK = %q, want 1", got)
	}
}

// TestClaudecode_SessionResume_PreservesActiveProvider is a regression test
// for cc-connect internal task t-20260614-qp7xnl: after a cc-connect process
// restart, calling SetActiveProvider with the name persisted on the session
// must restore providerEnv (ANTHROPIC_BASE_URL / ANTHROPIC_MODEL) so that the
// next --resume spawn does not silently send the user's switched-to model
// to the default provider's base URL.
func TestClaudecode_SessionResume_PreservesActiveProvider(t *testing.T) {
	providers := []core.ProviderConfig{
		{
			Name:    "default-prov",
			BaseURL: "https://default.example.com/anthropic",
			APIKey:  "default-key",
			Model:   "default-model",
		},
		{
			Name:    "minimax",
			BaseURL: "https://api.minimaxi.com/anthropic",
			APIKey:  "minimax-key",
			Model:   "MiniMax-M3",
		},
	}

	// Step 1: simulate the user's first message after `/provider switch minimax`.
	a1 := &Agent{providers: providers, activeIdx: -1}
	if !a1.SetActiveProvider("minimax") {
		t.Fatal("SetActiveProvider(minimax) returned false")
	}
	want := envSliceToMap(a1.providerEnvLocked())
	if got := want["ANTHROPIC_MODEL"]; got != "MiniMax-M3" {
		t.Fatalf("baseline ANTHROPIC_MODEL = %q, want MiniMax-M3", got)
	}
	if got := want["ANTHROPIC_BASE_URL"]; got != "https://api.minimaxi.com/anthropic" {
		t.Fatalf("baseline ANTHROPIC_BASE_URL = %q, want minimax", got)
	}

	// Step 2: simulate a cc-connect process restart. agent_session_id is
	// already on disk (carried by the engine via session.AgentSessionID), but
	// the in-memory activeIdx is back to -1.
	a2 := &Agent{providers: providers, activeIdx: -1}
	gotBefore := envSliceToMap(a2.providerEnvLocked())
	if _, hasModel := gotBefore["ANTHROPIC_MODEL"]; hasModel {
		t.Fatalf("post-restart pre-restore should have empty providerEnv, got %v", gotBefore)
	}

	// Step 3: engine looks up session.ActiveProvider and re-binds it before
	// calling StartSession with the saved sessionID.
	if !a2.SetActiveProvider("minimax") {
		t.Fatal("post-restart SetActiveProvider(minimax) returned false")
	}
	got := envSliceToMap(a2.providerEnvLocked())
	if got["ANTHROPIC_MODEL"] != want["ANTHROPIC_MODEL"] {
		t.Fatalf("post-restart ANTHROPIC_MODEL = %q, want %q", got["ANTHROPIC_MODEL"], want["ANTHROPIC_MODEL"])
	}
	if got["ANTHROPIC_BASE_URL"] != want["ANTHROPIC_BASE_URL"] {
		t.Fatalf("post-restart ANTHROPIC_BASE_URL = %q, want %q", got["ANTHROPIC_BASE_URL"], want["ANTHROPIC_BASE_URL"])
	}
	// The model name set in providerEnv must come from the second provider
	// (MiniMax-M3), not the first one (default-model). This is the exact
	// inversion the bug produced — model name from minimax sent to mimo's
	// base_url.
	if strings.Contains(got["ANTHROPIC_BASE_URL"], "default") {
		t.Fatalf("post-restart base URL leaked from default provider: %q", got["ANTHROPIC_BASE_URL"])
	}
}

func TestDetectEnvOnlyProviderType(t *testing.T) {
	tests := []struct {
		env      map[string]string
		expected string
	}{
		{map[string]string{"CLAUDE_CODE_USE_BEDROCK": "1"}, "bedrock"},
		{map[string]string{"CLAUDE_CODE_USE_VERTEX": "1"}, "vertex"},
		{map[string]string{"CLAUDE_CODE_USE_FOUNDRY": "1"}, "foundry"},
		{map[string]string{"CLAUDE_CODE_USE_BEDROCK": "0"}, ""},
		{map[string]string{"OTHER_VAR": "1"}, ""},
		{nil, ""},
	}

	for _, tt := range tests {
		got := detectEnvOnlyProviderType(tt.env)
		if got != tt.expected {
			t.Errorf("detectEnvOnlyProviderType(%v) = %q, want %q", tt.env, got, tt.expected)
		}
	}
}
