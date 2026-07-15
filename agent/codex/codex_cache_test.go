package codex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestAvailableModels_PrefersAppServerModelList(t *testing.T) {
	tmp := t.TempDir()
	fakeCodex := filepath.Join(tmp, "codex")
	script := `#!/bin/sh
while IFS= read -r line; do
  case "$line" in
    *'"method":"initialize"'*)
      printf '%s\n' '{"id":1,"result":{}}'
      ;;
    *'"method":"model/list"'*)
      printf '%s\n' '{"id":2,"result":{"data":[{"id":"gpt-5.6-sol","displayName":"GPT-5.6-Sol","description":"Latest frontier agentic coding model.","hidden":false},{"id":"hidden-model","displayName":"Hidden","hidden":true},{"model":"gpt-5.6-terra","displayName":"GPT-5.6-Terra","description":"Balanced agentic coding model.","hidden":false}]}}'
      exit 0
      ;;
  esac
done
`
	if err := os.WriteFile(fakeCodex, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	cache := `{"models":[{"slug":"gpt-5.4","visibility":"list","supported_in_api":true}]}`
	if err := os.WriteFile(filepath.Join(tmp, "models_cache.json"), []byte(cache), 0o600); err != nil {
		t.Fatalf("write models_cache.json: %v", err)
	}

	t.Setenv("CODEX_HOME", tmp)
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENAI_BASE_URL", "")

	a := &Agent{cliBin: fakeCodex, workDir: tmp, activeIdx: -1}
	models := a.AvailableModels(context.Background())
	if len(models) != 2 {
		t.Fatalf("models length = %d, want 2, models=%v", len(models), models)
	}
	if models[0].Name != "gpt-5.6-sol" || models[0].Alias != "GPT-5.6-Sol" {
		t.Fatalf("first model = %#v, want gpt-5.6-sol alias GPT-5.6-Sol", models[0])
	}
	if models[1].Name != "gpt-5.6-terra" || models[1].Alias != "GPT-5.6-Terra" {
		t.Fatalf("second model = %#v, want gpt-5.6-terra alias GPT-5.6-Terra", models[1])
	}
}

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

	a := &Agent{cliBin: filepath.Join(tmp, "missing-codex"), activeIdx: -1}
	models := a.AvailableModels(context.Background())
	if len(models) != 2 {
		t.Fatalf("models length = %d, want 2, models=%v", len(models), models)
	}
	if models[0].Name != "gpt-5.4" || models[1].Name != "gpt-5.3-codex" {
		t.Fatalf("models = %v, want [gpt-5.4 gpt-5.3-codex]", models)
	}
}
