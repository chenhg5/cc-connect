package claudecode

import (
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestNew_ParsesProjectEnvFromOpts(t *testing.T) {
	opts := map[string]any{
		"work_dir":    "/tmp/test",
		"run_as_user": "skip-lookpath",
		"env": map[string]string{
			"ANTHROPIC_BASE_URL":            "https://api.kimi.com/coding",
			"ANTHROPIC_AUTH_TOKEN":          "sk-kimi-test",
			"ANTHROPIC_MODEL":               "K2.6",
			"ANTHROPIC_REASONING_MODEL":     "K2.6",
			"ANTHROPIC_DEFAULT_HAIKU_MODEL": "K2.6",
		},
	}

	a, err := New(opts)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	agent := a.(*Agent)
	agent.mu.Lock()
	defer agent.mu.Unlock()

	if len(agent.sessionEnv) != 5 {
		t.Fatalf("expected 5 env vars, got %d: %v", len(agent.sessionEnv), agent.sessionEnv)
	}

	envMap := envSliceToMap(agent.sessionEnv)
	if got := envMap["ANTHROPIC_BASE_URL"]; got != "https://api.kimi.com/coding" {
		t.Errorf("ANTHROPIC_BASE_URL = %q, want %q", got, "https://api.kimi.com/coding")
	}
	if got := envMap["ANTHROPIC_AUTH_TOKEN"]; got != "sk-kimi-test" {
		t.Errorf("ANTHROPIC_AUTH_TOKEN = %q, want %q", got, "sk-kimi-test")
	}
	if got := envMap["ANTHROPIC_MODEL"]; got != "K2.6" {
		t.Errorf("ANTHROPIC_MODEL = %q, want %q", got, "K2.6")
	}
}

func TestNew_ParsesProjectEnvFromMapStringAny(t *testing.T) {
	opts := map[string]any{
		"work_dir":    "/tmp/test",
		"run_as_user": "test-user",
		"env": map[string]any{
			"ANTHROPIC_BASE_URL":   "https://api.mimo.com/v1",
			"ANTHROPIC_AUTH_TOKEN": "sk-mimo-test",
			"ANTHROPIC_MODEL":      "mimo-large",
		},
	}

	a, err := New(opts)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	agent := a.(*Agent)
	agent.mu.Lock()
	defer agent.mu.Unlock()

	envMap := envSliceToMap(agent.sessionEnv)
	if got := envMap["ANTHROPIC_BASE_URL"]; got != "https://api.mimo.com/v1" {
		t.Errorf("ANTHROPIC_BASE_URL = %q, want %q", got, "https://api.mimo.com/v1")
	}
	if got := envMap["ANTHROPIC_MODEL"]; got != "mimo-large" {
		t.Errorf("ANTHROPIC_MODEL = %q, want %q", got, "mimo-large")
	}
}

func TestNew_NoEnvOpts(t *testing.T) {
	opts := map[string]any{
		"work_dir":    "/tmp/test",
		"run_as_user": "test-user",
	}

	a, err := New(opts)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	agent := a.(*Agent)
	agent.mu.Lock()
	defer agent.mu.Unlock()

	if len(agent.sessionEnv) != 0 {
		t.Fatalf("expected 0 env vars when no env in opts, got %d: %v", len(agent.sessionEnv), agent.sessionEnv)
	}
}

func TestNew_ProjectEnvOverridesProviderEnv(t *testing.T) {
	opts := map[string]any{
		"work_dir":    "/tmp/test",
		"run_as_user": "test-user",
		"env": map[string]string{
			"ANTHROPIC_BASE_URL":   "https://api.deepseek.com/v1",
			"ANTHROPIC_AUTH_TOKEN": "sk-deepseek-test",
			"ANTHROPIC_MODEL":      "deepseek-chat",
		},
	}

	a, err := New(opts)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	agent := a.(*Agent)
	// Set providers to simulate a provider being configured
	agent.providers = []core.ProviderConfig{
		{
			Name:    "deepseek",
			BaseURL: "https://api.deepseek.com/v1",
			APIKey:  "sk-deepseek-test",
			Model:   "deepseek-chat",
		},
	}
	agent.activeIdx = 0

	// runtimeEnvLocked merges providerEnv + sessionEnv
	// sessionEnv (from opts["env"]) should be present
	env := agent.runtimeEnvLocked()
	envMap := envSliceToMap(env)

	if got := envMap["ANTHROPIC_BASE_URL"]; got != "https://api.deepseek.com/v1" {
		t.Errorf("ANTHROPIC_BASE_URL = %q, want %q", got, "https://api.deepseek.com/v1")
	}
	if got := envMap["ANTHROPIC_MODEL"]; got != "deepseek-chat" {
		t.Errorf("ANTHROPIC_MODEL = %q, want %q", got, "deepseek-chat")
	}
}
