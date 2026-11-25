package opencode

import (
	"encoding/json"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestNormalizeOpencodeReasoningEffort(t *testing.T) {
	cases := map[string]string{
		"none":         "none",
		"NONE":         "none",
		"off":          "none",
		"disabled":     "none",
		"min":          "minimal",
		"minimal":      "minimal",
		"low":          "low",
		"medium":       "medium",
		"med":          "medium",
		"high":         "high",
		"xhigh":        "xhigh",
		"x-high":       "xhigh",
		"extra-high":   "xhigh",
		" extra_high ": "xhigh",
		"bogus":        "",
		"":             "",
	}
	for in, want := range cases {
		if got := normalizeOpencodeReasoningEffort(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAddReasoningVariantsAddsAllLevels(t *testing.T) {
	in := `{"provider":{"aiapi":{"models":{"m":{"name":"m"}}}}}`
	out, ok := addReasoningVariants(in)
	if !ok {
		t.Fatal("expected config to be changed")
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(out), &cfg); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	model := cfg["provider"].(map[string]any)["aiapi"].(map[string]any)["models"].(map[string]any)["m"].(map[string]any)
	variants, _ := model["variants"].(map[string]any)
	if len(variants) != len(opencodeReasoningEfforts) {
		t.Fatalf("got %d variants, want %d", len(variants), len(opencodeReasoningEfforts))
	}
	for _, effort := range opencodeReasoningEfforts {
		v, ok := variants[effort].(map[string]any)
		if !ok {
			t.Fatalf("missing variant %q", effort)
		}
		if v["reasoningEffort"] != effort {
			t.Fatalf("variant %q reasoningEffort = %v, want %q", effort, v["reasoningEffort"], effort)
		}
	}
}

func TestAddReasoningVariantsPreservesExisting(t *testing.T) {
	in := `{"provider":{"p":{"models":{"m":{"variants":{"high":{"reasoningEffort":"high","textVerbosity":"low"},"custom":{"x":1}}}}}}}`
	out, ok := addReasoningVariants(in)
	if !ok {
		t.Fatal("expected config to be changed")
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(out), &cfg); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	variants := cfg["provider"].(map[string]any)["p"].(map[string]any)["models"].(map[string]any)["m"].(map[string]any)["variants"].(map[string]any)

	high := variants["high"].(map[string]any)
	if high["textVerbosity"] != "low" {
		t.Errorf("operator-configured high variant was overwritten: %v", high)
	}
	if _, ok := variants["custom"]; !ok {
		t.Error("operator-configured custom variant was dropped")
	}
	if len(variants) != len(opencodeReasoningEfforts)+1 {
		t.Errorf("got %d variants, want %d", len(variants), len(opencodeReasoningEfforts)+1)
	}
}

func TestAddReasoningVariantsNoDuplicate(t *testing.T) {
	first, ok := addReasoningVariants(`{"provider":{"p":{"models":{"m":{}}}}}`)
	if !ok {
		t.Fatal("first pass should change the config")
	}
	second, ok := addReasoningVariants(first)
	if ok {
		t.Errorf("second pass should be a no-op, got %s", second)
	}
}

func TestAddReasoningVariantsInvalidInput(t *testing.T) {
	for _, in := range []string{
		"",
		"not json",
		`null`,
		`{}`,
		`{"provider":{}}`,
		`{"provider":{"p":{}}}`,
		`{"provider":{"p":{"models":{}}}}`,
	} {
		if out, ok := addReasoningVariants(in); ok {
			t.Errorf("addReasoningVariants(%q) = (%q, true), want no change", in, out)
		}
	}
}

func TestInjectReasoningVariants(t *testing.T) {
	env := []string{"FOO=bar", `OPENCODE_CONFIG_CONTENT={"provider":{"p":{"models":{"m":{}}}}}`, "BAZ=qux"}

	if got := injectReasoningVariants(env, ""); len(got) != len(env) {
		t.Fatalf("empty effort should not rewrite env, got %v", got)
	}

	out := injectReasoningVariants(env, "high")
	if len(out) != len(env) {
		t.Fatalf("env length changed: %d -> %d", len(env), len(out))
	}
	if out[0] != "FOO=bar" || out[2] != "BAZ=qux" {
		t.Errorf("unrelated env entries were modified: %v", out)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(cutValue(t, out[1], opencodeConfigContentKey)), &cfg); err != nil {
		t.Fatalf("config value is not JSON: %v", err)
	}
	if _, ok := cfg["provider"]; !ok {
		t.Error("provider block lost")
	}
	if env[1] == out[1] {
		t.Error("original env slice value was mutated")
	}
}

func TestInjectReasoningVariantsNoConfig(t *testing.T) {
	env := []string{"FOO=bar"}
	got := injectReasoningVariants(env, "high")
	if len(got) != 1 || got[0] != "FOO=bar" {
		t.Errorf("expected env untouched, got %v", got)
	}
}

func TestAgentReasoningEffortSwitch(t *testing.T) {
	a := &Agent{model: "aiapi/deepseek-v4.1-flash"}

	if got := a.AvailableReasoningEfforts(); len(got) != len(opencodeReasoningEfforts) {
		t.Fatalf("available = %v, want all levels", got)
	}

	a.SetReasoningEffort("off")
	if got := a.GetReasoningEffort(); got != "none" {
		t.Errorf("after off, effort = %q, want none", got)
	}

	a.SetReasoningEffort("bogus")
	if got := a.GetReasoningEffort(); got != "none" {
		t.Errorf("invalid input must keep previous value, got %q", got)
	}

	a.SetReasoningEffort("HIGH")
	if got := a.GetReasoningEffort(); got != "high" {
		t.Errorf("GetReasoningEffort = %q, want high", got)
	}
}

func TestAvailableReasoningEffortsWithoutModel(t *testing.T) {
	a := &Agent{}
	if got := a.AvailableReasoningEfforts(); got != nil {
		t.Errorf("no model configured should return nil, got %v", got)
	}

	a = &Agent{activeIdx: 0, providers: []core.ProviderConfig{{Model: "p/m"}}}
	if got := a.AvailableReasoningEfforts(); len(got) != len(opencodeReasoningEfforts) {
		t.Errorf("provider model should enable switch, got %v", got)
	}
}

func cutValue(t *testing.T, kv, key string) string {
	t.Helper()
	for i := 0; i < len(kv); i++ {
		if kv[i] == '=' {
			if kv[:i] != key {
				t.Fatalf("key = %q, want %q", kv[:i], key)
			}
			return kv[i+1:]
		}
	}
	t.Fatalf("no '=' in %q", kv)
	return ""
}

func TestWorkspaceAgentOptionsCarriesReasoningEffort(t *testing.T) {
	a := &Agent{model: "p/m", activeIdx: -1}
	if _, ok := a.WorkspaceAgentOptions()["reasoning_effort"]; ok {
		t.Error("empty effort must not be propagated")
	}

	a.SetReasoningEffort("med")
	if got := a.WorkspaceAgentOptions()["reasoning_effort"]; got != "medium" {
		t.Errorf("workspace reasoning_effort = %v, want medium", got)
	}

	rebuilt, err := New(map[string]any{
		"work_dir":         t.TempDir(),
		"model":            "p/m",
		"reasoning_effort": "x-high",
	})
	if err != nil {
		t.Fatalf("rebuild from workspace options: %v", err)
	}
	got := rebuilt.(*Agent).GetReasoningEffort()
	if got != "xhigh" {
		t.Errorf("rebuilt agent effort = %q, want xhigh", got)
	}
}
