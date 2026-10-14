package claudecode

import (
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// TestFallbackModelsIncludesFable guards the picker fallback against losing the
// Claude 5 tier alias again: subscription-auth users never reach the provider
// API, so this list is the only source for them.
func TestFallbackModelsIncludesFable(t *testing.T) {
	models := fallbackModels()
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
	for _, m := range fallbackModels() {
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
func TestResolveTierModelHaikuFallsBackToSmallFast(t *testing.T) {
	t.Setenv("ANTHROPIC_DEFAULT_HAIKU_MODEL", "")
	t.Setenv("ANTHROPIC_SMALL_FAST_MODEL", "zed/claude-haiku-5-5[1m]")
	if got := resolveTierModel("haiku"); got != "zed/claude-haiku-5-5[1m]" {
		t.Errorf("resolveTierModel(haiku) = %q, want the small/fast model", got)
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
