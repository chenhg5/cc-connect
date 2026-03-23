package opencode

import (
	"sort"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestConfiguredModels_BoundaryConditions(t *testing.T) {
	a := &Agent{
		providers: []core.ProviderConfig{
			{Models: []core.ModelOption{{Name: "first"}}},
			{Models: []core.ModelOption{{Name: "second"}}},
		},
	}

	tests := []struct {
		name      string
		activeIdx int
		wantNil   bool
		wantName  string
	}{
		{name: "negative index", activeIdx: -1, wantNil: true},
		{name: "out of range", activeIdx: 2, wantNil: true},
		{name: "valid index", activeIdx: 1, wantName: "second"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a.activeIdx = tt.activeIdx
			got := a.configuredModels()
			if tt.wantNil {
				if got != nil {
					t.Fatalf("configuredModels() = %v, want nil", got)
				}
				return
			}
			if len(got) != 1 || got[0].Name != tt.wantName {
				t.Fatalf("configuredModels() = %v, want %q", got, tt.wantName)
			}
		})
	}
}

func TestNormalizeMode(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"yolo", "yolo"},
		{"YOLO", "yolo"},
		{"auto", "yolo"},
		{"AUTO", "yolo"},
		{"force", "yolo"},
		{"bypasspermissions", "yolo"},
		{"default", "default"},
		{"DEFAULT", "default"},
		{"", "default"},
		{"unknown", "default"},
		{"  yolo  ", "yolo"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeMode(tt.input)
			if got != tt.expected {
				t.Errorf("normalizeMode(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestAgent_Name(t *testing.T) {
	a := &Agent{}
	if got := a.Name(); got != "opencode" {
		t.Errorf("Name() = %q, want %q", got, "opencode")
	}
}

func TestAgent_SetModel(t *testing.T) {
	a := &Agent{}
	a.SetModel("gpt-4")
	if got := a.GetModel(); got != "gpt-4" {
		t.Errorf("GetModel() = %q, want %q", got, "gpt-4")
	}
}

func TestAgent_SetMode(t *testing.T) {
	a := &Agent{}
	a.SetMode("yolo")
	if got := a.GetMode(); got != "yolo" {
		t.Errorf("GetMode() = %q, want %q", got, "yolo")
	}
}

func TestAgent_GetActiveProvider(t *testing.T) {
	a := &Agent{
		providers: []core.ProviderConfig{
			{Name: "openai"},
			{Name: "anthropic"},
		},
		activeIdx: 1,
	}
	got := a.GetActiveProvider()
	if got == nil {
		t.Fatal("GetActiveProvider() returned nil")
	}
	if got.Name != "anthropic" {
		t.Errorf("GetActiveProvider().Name = %q, want %q", got.Name, "anthropic")
	}
}

func TestAgent_GetActiveProvider_NoActive(t *testing.T) {
	a := &Agent{
		providers: []core.ProviderConfig{
			{Name: "openai"},
		},
		activeIdx: -1,
	}
	if got := a.GetActiveProvider(); got != nil {
		t.Errorf("GetActiveProvider() = %v, want nil", got)
	}
}

func TestAgent_ListProviders(t *testing.T) {
	providers := []core.ProviderConfig{
		{Name: "openai"},
		{Name: "anthropic"},
	}
	a := &Agent{providers: providers}
	got := a.ListProviders()
	if len(got) != 2 {
		t.Errorf("ListProviders() returned %d providers, want 2", len(got))
	}
}

func TestAgent_SetActiveProvider(t *testing.T) {
	a := &Agent{
		providers: []core.ProviderConfig{
			{Name: "openai"},
			{Name: "anthropic"},
		},
	}
	if !a.SetActiveProvider("anthropic") {
		t.Error("SetActiveProvider(\"anthropic\") returned false")
	}
	if got := a.GetActiveProvider(); got == nil || got.Name != "anthropic" {
		t.Errorf("GetActiveProvider().Name = %q, want %q", got.Name, "anthropic")
	}
}

func TestAgent_SetActiveProvider_Invalid(t *testing.T) {
	a := &Agent{
		providers: []core.ProviderConfig{
			{Name: "openai"},
		},
	}
	if a.SetActiveProvider("nonexistent") {
		t.Error("SetActiveProvider(\"nonexistent\") returned true, want false")
	}
}

// verify Agent implements core.Agent
var _ core.Agent = (*Agent)(nil)

// -- providerEnvLocked tests --

// envMap converts a []string of "K=V" entries into a map for easier assertions.
func envMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, e := range env {
		for i := 0; i < len(e); i++ {
			if e[i] == '=' {
				m[e[:i]] = e[i+1:]
				break
			}
		}
	}
	return m
}

func TestProviderEnvLocked_NoActiveProvider(t *testing.T) {
	a := &Agent{
		providers: []core.ProviderConfig{{Name: "p1"}},
		activeIdx: -1,
	}
	got := a.providerEnvLocked()
	if got != nil {
		t.Errorf("providerEnvLocked() = %v, want nil", got)
	}
}

func TestProviderEnvLocked_APIKeyOnly(t *testing.T) {
	a := &Agent{
		providers: []core.ProviderConfig{
			{Name: "default-anthropic", APIKey: "sk-ant-test"},
		},
		activeIdx: 0,
	}
	env := envMap(a.providerEnvLocked())

	if env["ANTHROPIC_API_KEY"] != "sk-ant-test" {
		t.Errorf("ANTHROPIC_API_KEY = %q, want %q", env["ANTHROPIC_API_KEY"], "sk-ant-test")
	}
	if _, ok := env["ANTHROPIC_BASE_URL"]; ok {
		t.Error("ANTHROPIC_BASE_URL should not be set when BaseURL is empty")
	}
	if _, ok := env["ANTHROPIC_AUTH_TOKEN"]; ok {
		t.Error("ANTHROPIC_AUTH_TOKEN should not be set when BaseURL is empty")
	}
}

func TestProviderEnvLocked_BaseURLWithAPIKey(t *testing.T) {
	a := &Agent{
		providers: []core.ProviderConfig{
			{
				Name:    "third-party",
				APIKey:  "my-key",
				BaseURL: "https://my-proxy.example.com/v1",
			},
		},
		activeIdx: 0,
	}
	env := envMap(a.providerEnvLocked())

	if env["ANTHROPIC_BASE_URL"] != "https://my-proxy.example.com/v1" {
		t.Errorf("ANTHROPIC_BASE_URL = %q, want %q", env["ANTHROPIC_BASE_URL"], "https://my-proxy.example.com/v1")
	}
	// With BaseURL, APIKey should be sent as Bearer token
	if env["ANTHROPIC_AUTH_TOKEN"] != "my-key" {
		t.Errorf("ANTHROPIC_AUTH_TOKEN = %q, want %q", env["ANTHROPIC_AUTH_TOKEN"], "my-key")
	}
	// ANTHROPIC_API_KEY should be cleared to avoid x-api-key header
	if env["ANTHROPIC_API_KEY"] != "" {
		t.Errorf("ANTHROPIC_API_KEY = %q, want empty string", env["ANTHROPIC_API_KEY"])
	}
}

func TestProviderEnvLocked_BaseURLWithoutAPIKey(t *testing.T) {
	a := &Agent{
		providers: []core.ProviderConfig{
			{
				Name:    "no-key-proxy",
				BaseURL: "https://open-proxy.example.com/v1",
			},
		},
		activeIdx: 0,
	}
	env := envMap(a.providerEnvLocked())

	if env["ANTHROPIC_BASE_URL"] != "https://open-proxy.example.com/v1" {
		t.Errorf("ANTHROPIC_BASE_URL = %q, want %q", env["ANTHROPIC_BASE_URL"], "https://open-proxy.example.com/v1")
	}
	if _, ok := env["ANTHROPIC_AUTH_TOKEN"]; ok {
		t.Error("ANTHROPIC_AUTH_TOKEN should not be set when APIKey is empty")
	}
	if _, ok := env["ANTHROPIC_API_KEY"]; ok {
		t.Error("ANTHROPIC_API_KEY should not be set when APIKey is empty")
	}
}

func TestProviderEnvLocked_EnvPassthrough(t *testing.T) {
	a := &Agent{
		providers: []core.ProviderConfig{
			{
				Name:   "custom",
				APIKey: "key1",
				Env: map[string]string{
					"OPENAI_API_KEY":  "sk-openai",
					"OPENAI_BASE_URL": "https://openai-proxy.example.com",
					"CUSTOM_VAR":      "custom-value",
				},
			},
		},
		activeIdx: 0,
	}
	env := envMap(a.providerEnvLocked())

	// APIKey should be set (no BaseURL, so direct ANTHROPIC_API_KEY)
	if env["ANTHROPIC_API_KEY"] != "key1" {
		t.Errorf("ANTHROPIC_API_KEY = %q, want %q", env["ANTHROPIC_API_KEY"], "key1")
	}
	// Env map entries should all be present
	if env["OPENAI_API_KEY"] != "sk-openai" {
		t.Errorf("OPENAI_API_KEY = %q, want %q", env["OPENAI_API_KEY"], "sk-openai")
	}
	if env["OPENAI_BASE_URL"] != "https://openai-proxy.example.com" {
		t.Errorf("OPENAI_BASE_URL = %q, want %q", env["OPENAI_BASE_URL"], "https://openai-proxy.example.com")
	}
	if env["CUSTOM_VAR"] != "custom-value" {
		t.Errorf("CUSTOM_VAR = %q, want %q", env["CUSTOM_VAR"], "custom-value")
	}
}

func TestProviderEnvLocked_BaseURLWithThinking_ProxyStarts(t *testing.T) {
	// When Thinking is set, the proxy should start successfully and
	// ANTHROPIC_BASE_URL should point to the local proxy address.
	a := &Agent{
		providers: []core.ProviderConfig{
			{
				Name:     "thinking-provider",
				APIKey:   "tk-key",
				BaseURL:  "https://some-provider.example.com/v1",
				Thinking: "disabled",
			},
		},
		activeIdx: 0,
	}
	env := envMap(a.providerEnvLocked())
	t.Cleanup(func() { a.stopProviderProxyLocked() })

	// Proxy should have started; ANTHROPIC_BASE_URL should be a local address
	baseURL := env["ANTHROPIC_BASE_URL"]
	if baseURL == "" {
		t.Fatal("ANTHROPIC_BASE_URL is empty, expected local proxy URL")
	}
	if baseURL == "https://some-provider.example.com/v1" {
		t.Error("ANTHROPIC_BASE_URL points to target directly; expected local proxy URL")
	}
	if len(baseURL) < 7 || baseURL[:7] != "http://" {
		t.Errorf("ANTHROPIC_BASE_URL = %q, expected http://127.0.0.1:PORT", baseURL)
	}
	// NO_PROXY should be set for the local proxy
	if env["NO_PROXY"] != "127.0.0.1" {
		t.Errorf("NO_PROXY = %q, want %q", env["NO_PROXY"], "127.0.0.1")
	}
	// Bearer auth should still be set
	if env["ANTHROPIC_AUTH_TOKEN"] != "tk-key" {
		t.Errorf("ANTHROPIC_AUTH_TOKEN = %q, want %q", env["ANTHROPIC_AUTH_TOKEN"], "tk-key")
	}
	// ANTHROPIC_API_KEY should be cleared
	if env["ANTHROPIC_API_KEY"] != "" {
		t.Errorf("ANTHROPIC_API_KEY = %q, want empty", env["ANTHROPIC_API_KEY"])
	}
	// Agent should have a proxy reference
	if a.providerProxy == nil {
		t.Error("providerProxy is nil after successful proxy start")
	}
	if a.proxyLocalURL == "" {
		t.Error("proxyLocalURL is empty after successful proxy start")
	}
}

func TestProviderEnvLocked_BaseURLWithThinking_ProxyFallback(t *testing.T) {
	// When Thinking is set but proxy fails to start (bad URL that can't be
	// parsed), the function should fall back to setting ANTHROPIC_BASE_URL
	// directly.
	a := &Agent{
		providers: []core.ProviderConfig{
			{
				Name:     "bad-url-provider",
				APIKey:   "tk-key",
				BaseURL:  "://missing-scheme",
				Thinking: "disabled",
			},
		},
		activeIdx: 0,
	}
	env := envMap(a.providerEnvLocked())

	// Should fall back to direct BaseURL since proxy can't parse the URL
	if env["ANTHROPIC_BASE_URL"] != "://missing-scheme" {
		t.Errorf("ANTHROPIC_BASE_URL = %q, want %q (fallback)", env["ANTHROPIC_BASE_URL"], "://missing-scheme")
	}
	if env["ANTHROPIC_AUTH_TOKEN"] != "tk-key" {
		t.Errorf("ANTHROPIC_AUTH_TOKEN = %q, want %q", env["ANTHROPIC_AUTH_TOKEN"], "tk-key")
	}
}

func TestProviderEnvLocked_OutOfRange(t *testing.T) {
	a := &Agent{
		providers: []core.ProviderConfig{
			{Name: "only"},
		},
		activeIdx: 5,
	}
	got := a.providerEnvLocked()
	if got != nil {
		t.Errorf("providerEnvLocked() = %v, want nil for out-of-range index", got)
	}
}

func TestProviderEnvLocked_EnvOverridesAPIKey(t *testing.T) {
	// If Env explicitly sets ANTHROPIC_API_KEY, it should override the
	// auto-generated one (Env entries are appended after the APIKey entry).
	a := &Agent{
		providers: []core.ProviderConfig{
			{
				Name:   "env-override",
				APIKey: "auto-key",
				Env: map[string]string{
					"ANTHROPIC_API_KEY": "override-key",
				},
			},
		},
		activeIdx: 0,
	}
	raw := a.providerEnvLocked()

	// The last occurrence of ANTHROPIC_API_KEY should win in most env
	// implementations. Verify both entries exist in the expected order.
	var keys []string
	for _, e := range raw {
		if len(e) > len("ANTHROPIC_API_KEY=") && e[:len("ANTHROPIC_API_KEY=")] == "ANTHROPIC_API_KEY=" {
			keys = append(keys, e)
		}
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 ANTHROPIC_API_KEY entries, got %d: %v", len(keys), keys)
	}
	if keys[0] != "ANTHROPIC_API_KEY=auto-key" {
		t.Errorf("first ANTHROPIC_API_KEY = %q, want %q", keys[0], "ANTHROPIC_API_KEY=auto-key")
	}
	if keys[1] != "ANTHROPIC_API_KEY=override-key" {
		t.Errorf("second ANTHROPIC_API_KEY = %q, want %q", keys[1], "ANTHROPIC_API_KEY=override-key")
	}
}

func TestStopProviderProxyLocked_Idempotent(t *testing.T) {
	a := &Agent{}
	// Calling stopProviderProxyLocked on a nil proxy should not panic
	a.stopProviderProxyLocked()
	a.stopProviderProxyLocked()
}

func TestSetActiveProvider_ClearsProxy(t *testing.T) {
	a := &Agent{
		providers: []core.ProviderConfig{
			{Name: "p1"},
			{Name: "p2"},
		},
		activeIdx:     0,
		proxyLocalURL: "http://127.0.0.1:12345",
		// providerProxy is nil but proxyLocalURL is set —
		// SetActiveProvider should clear proxyLocalURL
	}
	a.SetActiveProvider("p2")
	if a.proxyLocalURL != "" {
		t.Errorf("proxyLocalURL = %q, want empty after provider switch", a.proxyLocalURL)
	}
}

func TestStop_ClearsProxy(t *testing.T) {
	a := &Agent{
		proxyLocalURL: "http://127.0.0.1:12345",
	}
	if err := a.Stop(); err != nil {
		t.Fatalf("Stop() returned error: %v", err)
	}
	if a.proxyLocalURL != "" {
		t.Errorf("proxyLocalURL = %q, want empty after Stop()", a.proxyLocalURL)
	}
}

func TestProviderEnvLocked_EnvKeysAreSorted(t *testing.T) {
	// Verify env entries from the Env map are all present (order doesn't
	// matter for env vars, but all must be emitted).
	a := &Agent{
		providers: []core.ProviderConfig{
			{
				Name: "multi-env",
				Env: map[string]string{
					"A_VAR": "a",
					"B_VAR": "b",
					"C_VAR": "c",
				},
			},
		},
		activeIdx: 0,
	}
	env := envMap(a.providerEnvLocked())
	wantKeys := []string{"A_VAR", "B_VAR", "C_VAR"}
	var gotKeys []string
	for k := range env {
		gotKeys = append(gotKeys, k)
	}
	sort.Strings(gotKeys)
	sort.Strings(wantKeys)
	if len(gotKeys) != len(wantKeys) {
		t.Fatalf("got keys %v, want %v", gotKeys, wantKeys)
	}
	for i := range wantKeys {
		if gotKeys[i] != wantKeys[i] {
			t.Errorf("key[%d] = %q, want %q", i, gotKeys[i], wantKeys[i])
		}
	}
}
