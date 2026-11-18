package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func nativeModelTestAgent(t *testing.T, scenario string) *Agent {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	t.Setenv("CC_CODEX_MODELS_HELPER", "1")
	return &Agent{
		cmd: bin, activeIdx: -1, workDir: home,
		cliExtraArgs: []string{"-test.run=^TestNativeModelsCLIProcess$", "--", scenario},
	}
}

func TestAvailableModels_NativeListOverridesStaleCacheAndGenericAPI(t *testing.T) {
	a := nativeModelTestAgent(t, "pages")
	home := os.Getenv("CODEX_HOME")
	stale := []byte(`{"models":[{"slug":"stale-audio-model","visibility":"list","supported_in_api":true}]}`)
	for _, file := range []string{"models_cache.json", "catalog.json"} {
		if err := os.WriteFile(filepath.Join(home, file), stale, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("model_catalog_json = \"catalog.json\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("generic API must not override a successful native catalog")
		_, _ = fmt.Fprint(w, `{"data":[{"id":"gpt-generic"}]}`)
	}))
	defer srv.Close()
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	want := []core.ModelOption{{Name: "native-first", Desc: "Native description"}, {Name: "native-second"}}
	if got := a.AvailableModels(context.Background()); !reflect.DeepEqual(got, want) {
		t.Fatalf("native model list = %#v, want %#v; stale cache must not win", got, want)
	}
}

func TestAvailableModels_ExplicitProviderModelsOverrideNativeCatalog(t *testing.T) {
	a := nativeModelTestAgent(t, "unexpected")
	a.activeIdx = 0
	want := []core.ModelOption{{Name: "provider-choice"}}
	a.providers = []core.ProviderConfig{{Models: want}}
	if got := a.AvailableModels(context.Background()); !reflect.DeepEqual(got, want) {
		t.Fatalf("explicit model list = %#v, want %#v", got, want)
	}
}

func TestNativeModels_UsesTurnCLIProfileHomeAndEnvironment(t *testing.T) {
	a := nativeModelTestAgent(t, "config")
	home := t.TempDir()
	a.codexHome = home
	a.configEnv = []string{"CC_MODEL_ENV=config", "CC_EXPECT_MODEL_HOME=" + home}
	a.sessionEnv = []string{"CC_MODEL_ENV=session"}
	a.activeIdx = 0
	a.providers = []core.ProviderConfig{{
		Name: "custom", BaseURL: "https://example.invalid", APIKey: "fixture-key",
		Env: map[string]string{"CC_MODEL_ENV": "provider"},
	}}
	a.cliExtraArgs = append(a.cliExtraArgs, "-p", "custom-profile")
	models, err := a.loadModelsFromCLI(context.Background())
	if err != nil || len(models) != 1 || models[0].Name != "config-ok" {
		t.Fatalf("model metadata did not use turn configuration: models=%v err=%v", models, err)
	}
}

func TestNativeModels_FailureFallsBackWithoutStartingTurns(t *testing.T) {
	for _, scenario := range []string{"unsupported", "empty", "cursor-loop", "hang"} {
		t.Run(scenario, func(t *testing.T) {
			a := nativeModelTestAgent(t, scenario)
			t.Setenv("OPENAI_API_KEY", "")
			cache := []byte(`{"models":[{"slug":"offline-choice","visibility":"list","supported_in_api":true}]}`)
			if err := os.WriteFile(filepath.Join(os.Getenv("CODEX_HOME"), "models_cache.json"), cache, 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancel()
			start := time.Now()
			models := a.AvailableModels(ctx)
			if len(models) != 1 || models[0].Name != "offline-choice" {
				t.Fatalf("fallback models = %v", models)
			}
			if time.Since(start) > 3*time.Second {
				t.Fatal("native discovery ignored cancellation")
			}
		})
	}
}

// Run the test executable as a deterministic stdio app-server. Any attempt
// to start a thread or turn fails, so discovery cannot spend model tokens.
func TestNativeModelsCLIProcess(t *testing.T) {
	if os.Getenv("CC_CODEX_MODELS_HELPER") != "1" {
		return
	}
	i := 0
	for i < len(os.Args) && os.Args[i] != "--" {
		i++
	}
	if i+1 >= len(os.Args) {
		os.Exit(30)
	}
	scenario := os.Args[i+1]
	if scenario == "config" {
		args := strings.Join(os.Args[i+2:], " ")
		if !strings.Contains(args, "-p custom-profile") || !strings.Contains(args, `model_provider="custom"`) ||
			!strings.Contains(args, `openai_base_url="https://example.invalid"`) ||
			os.Getenv("CODEX_HOME") != os.Getenv("CC_EXPECT_MODEL_HOME") || os.Getenv("CC_MODEL_ENV") != "session" ||
			os.Getenv("OPENAI_API_KEY") != "fixture-key" {
			os.Exit(31)
		}
	}
	initialized := false
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
			Params struct {
				IncludeHidden bool   `json:"includeHidden"`
				Cursor        string `json:"cursor"`
			} `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			os.Exit(32)
		}
		var result any = map[string]any{}
		switch req.Method {
		case "initialize":
		case "initialized":
			initialized = true
			continue
		case "model/list":
			if !initialized || req.Params.IncludeHidden {
				os.Exit(33)
			}
			switch scenario {
			case "unsupported":
				_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"id": req.ID, "error": map[string]any{"code": -32601, "message": "unsupported"}})
				continue
			case "hang":
				time.Sleep(time.Minute)
			case "empty":
				result = map[string]any{"data": []any{}}
			case "config":
				result = map[string]any{"data": []any{map[string]any{"model": "config-ok"}}}
			case "pages", "cursor-loop":
				if req.Params.Cursor == "" {
					result = map[string]any{"data": []any{
						map[string]any{"id": "opaque-id", "model": "native-first", "description": "Native description"},
						map[string]any{"model": "hidden-model", "hidden": true},
					}, "nextCursor": "second-page"}
				} else {
					result = map[string]any{"data": []any{
						map[string]any{"model": "native-first"}, map[string]any{"id": "native-second"}, map[string]any{},
					}}
					if scenario == "cursor-loop" {
						result.(map[string]any)["nextCursor"] = "second-page"
					}
				}
			default:
				os.Exit(34)
			}
		default:
			os.Exit(35)
		}
		if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"id": req.ID, "result": result}); err != nil {
			os.Exit(36)
		}
	}
	os.Exit(0)
}
