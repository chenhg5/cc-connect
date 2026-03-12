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

