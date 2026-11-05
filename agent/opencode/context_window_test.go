package opencode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func writeModelsCatalogue(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "opencode"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "opencode", "models.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const testCatalogue = `{
  "openai": {"id": "openai", "models": {
    "gpt-6.1-sol": {"id": "gpt-6.1-sol", "limit": {"context": 1050000, "output": 128000}},
    "no-limit": {"id": "no-limit"}
  }},
  "deepseek": {"models": {"deepseek-flash": {"limit": {"context": 128000}}}}
}`

func TestOpencodeContextWindow_LooksUpTheCatalogue(t *testing.T) {
	writeModelsCatalogue(t, testCatalogue)
	cases := map[string]int{
		"openai/gpt-6.1-sol":       1050000,
		" deepseek/deepseek-flash": 128000,
		"openai/no-limit":          0,
		"openai/unknown":           0,
		"custom/gpt-6.1-sol":       0, // another provider may serve a different window
		"gpt-6.1-sol":              0, // no provider: ambiguous
		"":                         0,
	}
	for model, want := range cases {
		if got := opencodeContextWindow(model); got != want {
			t.Errorf("opencodeContextWindow(%q) = %d, want %d", model, got, want)
		}
	}
}

func TestOpencodeContextWindow_NoCatalogue(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if got := opencodeContextWindow("openai/gpt-6.1-sol"); got != 0 {
		t.Fatalf("window without a catalogue = %d, want 0", got)
	}
}

// TestGetContextUsage_ReportsTheModelWindow pins the footer fix: a 230k prompt on
// a 1M-window model must not be measured against the engine's generic 200k
// (which showed ~100%), so the usage carries the model's real window.
func TestGetContextUsage_ReportsTheModelWindow(t *testing.T) {
	writeModelsCatalogue(t, testCatalogue)
	s := &opencodeSession{model: "openai/gpt-6.1-sol"}
	if s.GetContextUsage() != nil {
		t.Fatal("no usage yet must stay nil")
	}
	s.usage = &core.ContextUsage{UsedTokens: 230982, InputTokens: 347, CachedInputTokens: 230272}
	got := s.GetContextUsage()
	if got == nil || got.ContextWindow != 1050000 || got.UsedTokens != 230982 {
		t.Fatalf("usage = %+v, want ContextWindow 1050000 and UsedTokens kept", got)
	}
	if s.usage.ContextWindow != 0 {
		t.Fatal("GetContextUsage must not write into the session's own usage")
	}

	unknown := &opencodeSession{model: "custom/model", usage: &core.ContextUsage{UsedTokens: 1000}}
	if w := unknown.GetContextUsage().ContextWindow; w != 0 {
		t.Fatalf("unknown model window = %d, want 0 (engine keeps its estimate)", w)
	}
}
