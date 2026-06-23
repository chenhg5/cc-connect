package codex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestAvailableModels_FallbackToModelsCache(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	tmp := t.TempDir()
	cache := `{
  "models": [
    {"slug":"gpt-5.4","description":"Latest frontier agentic coding model.","visibility":"list","supported_in_api":true},
    {"slug":"gpt-5.3-codex","description":"Great for coding","visibility":"list","supported_in_api":true},
    {"slug":"hidden-internal","visibility":"hidden","supported_in_api":true},
    {"slug":"tool-only","visibility":"list","supported_in_api":false}
  ]
}`
	if err := os.WriteFile(tmp+"/models_cache.json", []byte(cache), 0o600); err != nil {
		t.Fatalf("write models_cache.json: %v", err)
	}

	t.Setenv("CODEX_HOME", tmp)
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_BASE_URL", srv.URL)

	a := &Agent{activeIdx: -1}
	models := a.AvailableModels(context.Background())
	if len(models) != 2 {
		t.Fatalf("models length = %d, want 2, models=%v", len(models), models)
	}
	if models[0].Name != "gpt-5.4" || models[1].Name != "gpt-5.3-codex" {
		t.Fatalf("models = %v, want [gpt-5.4 gpt-5.3-codex]", models)
	}
}

// TestAvailableModels_UsesModelCatalog tests that when CODEX_HOME/config.toml
// contains model_catalog_json, readCodexModelCatalog reads and parses that file
// as the highest-priority model source.
func TestAvailableModels_UsesModelCatalog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	tmp := t.TempDir()

	config := `model_catalog_json = "model_catalog.json"
`
	if err := os.WriteFile(tmp+"/config.toml", []byte(config), 0o644); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}

	// Mix of models: list-visible API-supported, hidden, non-API, and
	// one with an empty slug that falls back to display_name.
	catalog := `{
  "models": [
    {"slug":"gpt-5.4","display_name":"GPT-5.4","description":"Latest frontier agentic coding model.","visibility":"list","supported_in_api":true},
    {"slug":"gpt-5.3-codex","display_name":"GPT-5.3 Codex","description":"Great for coding","visibility":"list","supported_in_api":true},
    {"slug":"hidden-internal","visibility":"hidden","supported_in_api":true},
    {"slug":"tool-only","visibility":"list","supported_in_api":false},
    {"slug":"","display_name":"MissingSlug","visibility":"list","supported_in_api":true}
  ]
}`
	if err := os.WriteFile(tmp+"/model_catalog.json", []byte(catalog), 0o644); err != nil {
		t.Fatalf("write model_catalog.json: %v", err)
	}

	t.Setenv("CODEX_HOME", tmp)
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_BASE_URL", srv.URL)

	a := &Agent{activeIdx: -1}
	models := a.AvailableModels(context.Background())

	// 3 visible+supported: gpt-5.4, gpt-5.3-codex, MissingSlug (slug empty → display_name fallback)
	if len(models) != 3 {
		t.Fatalf("models length = %d, want 3, models=%v", len(models), models)
	}
	if models[0].Name != "gpt-5.4" || models[0].Desc != "Latest frontier agentic coding model." {
		t.Errorf("models[0] = %+v, want gpt-5.4 with description", models[0])
	}
	if models[1].Name != "gpt-5.3-codex" || models[1].Desc != "Great for coding" {
		t.Errorf("models[1] = %+v, want gpt-5.3-codex with description", models[1])
	}
	if models[2].Name != "MissingSlug" || models[2].Desc != "" {
		t.Errorf("models[2] = %+v, want MissingSlug (display_name fallback)", models[2])
	}
}

// TestReadCodexModelCatalog_NoConfigFile tests graceful fallback when
// CODEX_HOME/config.toml does not exist.
func TestReadCodexModelCatalog_NoConfigFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CODEX_HOME", tmp)

	a := &Agent{activeIdx: -1}
	models := a.AvailableModels(context.Background())

	// No config.toml → no model_catalog.json → no models_cache.json
	// → no OPENAI_API_KEY → all the way to hardcoded fallback (6 models)
	if len(models) != 6 {
		t.Fatalf("expected 6 hardcoded fallback models, got %d: %v", len(models), models)
	}
}
