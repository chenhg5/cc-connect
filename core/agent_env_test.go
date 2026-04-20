package core

import (
	"sort"
	"strings"
	"testing"
)

func TestAgentEnvFromOpts_Nil(t *testing.T) {
	if got := AgentEnvFromOpts(nil); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
	if got := AgentEnvFromOpts(map[string]any{}); got != nil {
		t.Fatalf("expected nil for empty opts, got %v", got)
	}
	if got := AgentEnvFromOpts(map[string]any{"work_dir": "/tmp"}); got != nil {
		t.Fatalf("expected nil when env not set, got %v", got)
	}
}

func TestAgentEnvFromOpts_MapStringAny(t *testing.T) {
	opts := map[string]any{
		"work_dir": "/tmp",
		"env": map[string]any{
			"HTTP_PROXY":  "http://127.0.0.1:7890",
			"MY_CUSTOM":   "hello",
		},
	}
	env := AgentEnvFromOpts(opts)
	m := envSliceToMapAgentEnv(env)

	if got := m["HTTP_PROXY"]; got != "http://127.0.0.1:7890" {
		t.Errorf("HTTP_PROXY = %q", got)
	}
	if got := m["MY_CUSTOM"]; got != "hello" {
		t.Errorf("MY_CUSTOM = %q", got)
	}
}

func TestAgentEnvFromOpts_MapStringString(t *testing.T) {
	opts := map[string]any{
		"env": map[string]string{
			"FOO": "bar",
		},
	}
	env := AgentEnvFromOpts(opts)
	m := envSliceToMapAgentEnv(env)

	if got := m["FOO"]; got != "bar" {
		t.Errorf("FOO = %q", got)
	}
}

func TestAgentEnvFromOpts_OrderConsistent(t *testing.T) {
	opts := map[string]any{
		"env": map[string]any{
			"Z_VAR": "1",
			"A_VAR": "2",
			"M_VAR": "3",
		},
	}
	env := AgentEnvFromOpts(opts)
	if len(env) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(env))
	}
	// Verify all keys are present regardless of order
	keys := make([]string, 0, len(env))
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := []string{"A_VAR", "M_VAR", "Z_VAR"}
	if len(keys) != len(want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	for i := range keys {
		if keys[i] != want[i] {
			t.Errorf("keys[%d] = %q, want %q", i, keys[i], want[i])
		}
	}
}

func envSliceToMapAgentEnv(env []string) map[string]string {
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
