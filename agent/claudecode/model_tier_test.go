package claudecode

import (
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// TestFallbackModelsIncludesFable guards the picker fallback against losing the
// Claude 5 tier alias again: subscription-auth users never reach the provider
// API, so this list is the only source for them.
func TestFallbackModelsIncludesFable(t *testing.T) {
	models := (&Agent{}).fallbackModels()
	if len(models) == 0 {
		t.Fatal("fallbackModels returned no models")
	}
	seen := make(map[string]core.ModelOption, len(models))
	for _, m := range models {
		seen[m.Name] = m
	}
	for _, want := range []string{"fable", "opus", "sonnet", "haiku", "fable[1m]", "opus[1m]", "sonnet[1m]"} {
		if _, ok := seen[want]; !ok {
			t.Errorf("fallback list is missing tier %q", want)
		}
	}
}

// TestFallbackModelsShowsResolvedTierModel asserts the picker surfaces the
// concrete backend a tier resolves to, so "sonnet" does not hide the fact that
// it runs on a gateway model.
func TestFallbackModelsShowsResolvedTierModel(t *testing.T) {
	t.Setenv("ANTHROPIC_DEFAULT_SONNET_MODEL", "deepseek-v4.1-flash")
	t.Setenv("ANTHROPIC_DEFAULT_FABLE_MODEL", "workbuddy-ai/deepseek-v4.1-flash[1m]")

	byName := make(map[string]core.ModelOption)
	for _, m := range (&Agent{}).fallbackModels() {
		byName[m.Name] = m
	}

	if got := byName["sonnet"].Desc; got != "deepseek-v4.1-flash" {
		t.Errorf("sonnet Desc = %q, want the resolved model", got)
	}
	if got := byName["sonnet[1m]"].Desc; got != "deepseek-v4.1-flash (1M context)" {
		t.Errorf("sonnet[1m] Desc = %q, want resolved model + context suffix", got)
	}
	if got := byName["fable"].Desc; got != "workbuddy-ai/deepseek-v4.1-flash[1m]" {
		t.Errorf("fable Desc = %q, want the resolved model", got)
	}
}

// TestResolveTierModelHaikuFallsBackToSmallFast covers the one tier that has a
// second source: Claude Code uses the small/fast model for haiku-class work.
func TestResolveTierModelHaikuUsesDefaultHaikuVar(t *testing.T) {
	t.Setenv("ANTHROPIC_DEFAULT_HAIKU_MODEL", "zed/claude-haiku-5-5[1m]")
	t.Setenv("ANTHROPIC_SMALL_FAST_MODEL", "deprecated-background-model")
	if got := (&Agent{}).resolveTierModel("haiku"); got != "zed/claude-haiku-5-5[1m]" {
		t.Errorf("resolveTierModel(haiku) = %q, want ANTHROPIC_DEFAULT_HAIKU_MODEL", got)
	}
}

// TestReasoningEffortsIncludeXhighAndUltracode pins the menu to the levels the
// current CLI accepts.
func TestReasoningEffortsIncludeXhighAndUltracode(t *testing.T) {
	var a Agent
	got := a.AvailableReasoningEfforts()
	want := map[string]bool{"low": true, "medium": true, "high": true, "xhigh": true, "max": true, "ultracode": true}
	if len(got) != len(want) {
		t.Fatalf("AvailableReasoningEfforts() = %v, want %d levels", got, len(want))
	}
	for _, e := range got {
		if !want[e] {
			t.Errorf("unexpected effort level %q", e)
		}
	}
}

// TestNormalizeEffortKeepsNewLevels guards against the silent-drop regression:
// unknown values normalize to "" and are never passed to the CLI.
func TestNormalizeEffortKeepsNewLevels(t *testing.T) {
	for _, in := range []string{"xhigh", "ultracode", "low", "medium", "high", "max"} {
		if got := normalizeEffort(in); got == "" {
			t.Errorf("normalizeEffort(%q) dropped the value", in)
		}
	}
	if got := normalizeEffort("bogus"); got != "" {
		t.Errorf("normalizeEffort(bogus) = %q, want empty", got)
	}
}

// TestSubagentModelEnvInjection checks the override reaches the spawned CLI and
// that an empty value sends nothing (inherit).
func TestSubagentModelEnvInjection(t *testing.T) {
	a := &Agent{subagentModel: "stepfun-cn/step-3.7-flash[1m]"}
	env := a.runtimeEnvLocked()
	found := ""
	for _, kv := range env {
		if len(kv) > len("CLAUDE_CODE_SUBAGENT_MODEL=") && kv[:len("CLAUDE_CODE_SUBAGENT_MODEL=")] == "CLAUDE_CODE_SUBAGENT_MODEL=" {
			found = kv
		}
	}
	if found != "CLAUDE_CODE_SUBAGENT_MODEL=stepfun-cn/step-3.7-flash[1m]" {
		t.Errorf("env injection = %q", found)
	}

	a.SetSubagentModel("")
	for _, kv := range a.runtimeEnvLocked() {
		if len(kv) >= 26 && kv[:26] == "CLAUDE_CODE_SUBAGENT_MODEL" {
			t.Errorf("empty override should send nothing, got %q", kv)
		}
	}
}

// TestFallbackModelsKeepsMainOrder pins the first six entries to main's list so
// /model switch <n> does not silently point at a different model after the
// picker refresh. New tiers must be appended, never inserted.
func TestFallbackModelsKeepsMainOrder(t *testing.T) {
	want := []string{"sonnet", "sonnet[1m]", "opus", "opus[1m]", "fable", "haiku"}
	got := (&Agent{}).fallbackModels()
	if len(got) < len(want) {
		t.Fatalf("fallbackModels() has %d entries, want at least %d", len(got), len(want))
	}
	for i, name := range want {
		if got[i].Name != name {
			t.Errorf("entry %d = %q, want %q (main order)", i, got[i].Name, name)
		}
	}
	if got[len(want)].Name != "fable[1m]" {
		t.Errorf("first appended entry = %q, want fable[1m]", got[len(want)].Name)
	}
}

// TestReasoningEffortsKeepMainOrder pins the first four levels so /effort <n>
// keeps its meaning; the new levels are appended.
func TestReasoningEffortsKeepMainOrder(t *testing.T) {
	var a Agent
	got := a.AvailableReasoningEfforts()
	want := []string{"low", "medium", "high", "max", "xhigh", "ultracode"}
	if len(got) != len(want) {
		t.Fatalf("AvailableReasoningEfforts() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("level %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestFallbackModelsResolvesTierFromConfigEnv covers the documented way to pin
// a tier: [projects.agent.options.env], which never reaches the supervisor's
// own process environment.
func TestFallbackModelsResolvesTierFromConfigEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_DEFAULT_SONNET_MODEL", "")
	a := &Agent{configEnv: []string{"ANTHROPIC_DEFAULT_SONNET_MODEL=K2.6"}}
	for _, m := range a.fallbackModels() {
		if m.Name == "sonnet" {
			if m.Desc != "K2.6" {
				t.Fatalf("sonnet Desc = %q, want K2.6 from config env", m.Desc)
			}
			return
		}
	}
	t.Fatal("sonnet not in fallback list")
}

// TestFallbackModelsResolvesTierFromProviderEnv covers a provider's env map,
// which is merged into the child environment after the config env.
func TestFallbackModelsResolvesTierFromProviderEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_DEFAULT_OPUS_MODEL", "")
	a := &Agent{
		activeIdx: 0,
		providers: []core.ProviderConfig{{
			Name: "gw",
			Env:  map[string]string{"ANTHROPIC_DEFAULT_OPUS_MODEL": "K2.6-opus"},
		}},
	}
	for _, m := range a.fallbackModels() {
		if m.Name == "opus" {
			if m.Desc != "K2.6-opus" {
				t.Fatalf("opus Desc = %q, want K2.6-opus from provider env", m.Desc)
			}
			return
		}
	}
	t.Fatal("opus not in fallback list")
}

// TestEffectiveEnvValuePrecedence pins the order the CLI receives: session env
// beats provider env, which beats config env, which beats the process env.
func TestEffectiveEnvValuePrecedence(t *testing.T) {
	const key = "ANTHROPIC_DEFAULT_SONNET_MODEL"
	t.Setenv(key, "process")
	a := &Agent{
		configEnv: []string{key + "=config"},
		providers: []core.ProviderConfig{{Name: "gw", Env: map[string]string{key: "provider"}}},
		activeIdx: 0,
	}
	if got := a.effectiveEnvValue(key); got != "provider" {
		t.Fatalf("provider env should beat config env, got %q", got)
	}
	a.sessionEnv = []string{key + "=session"}
	if got := a.effectiveEnvValue(key); got != "session" {
		t.Fatalf("session env should win, got %q", got)
	}
}

// TestEffectiveEnvValueExplicitEmptyClearsPin covers the edge case where a
// higher-priority source deliberately clears a pin: the child receives KEY=,
// so the picker must stop there instead of showing a stale lower source.
func TestEffectiveEnvValueExplicitEmptyClearsPin(t *testing.T) {
	const key = "ANTHROPIC_DEFAULT_SONNET_MODEL"
	t.Setenv(key, "process-pin")
	a := &Agent{configEnv: []string{key + "="}}
	if got := a.effectiveEnvValue(key); got != "" {
		t.Fatalf("explicit empty config value should clear the pin, got %q", got)
	}
}
