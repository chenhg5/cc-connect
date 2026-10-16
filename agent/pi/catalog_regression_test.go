package pi

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestLookupModelDef_PreservesColonIDs(t *testing.T) {
	writePiModelFiles(t, nil, nil, map[string]any{"openrouter": map[string]any{"models": []any{
		map[string]any{"id": "vendor/chat:free"},
		map[string]any{"id": "vendor/chat:high"},
		map[string]any{"id": "vendor/chat", "reasoning": true},
	}}})
	for _, ref := range []string{"openrouter/vendor/chat:free", "openrouter/vendor/chat:high", "openrouter/vendor/chat:free:high"} {
		t.Run(ref, func(t *testing.T) {
			a := &Agent{model: ref}
			if got := a.AvailableReasoningEfforts(); fmt.Sprint(got) != "[off]" {
				t.Fatalf("efforts = %v, want [off]", got)
			}
		})
	}
	if _, _, ok := lookupModelDef("openrouter/vendor/chat:unknown"); ok {
		t.Fatal("unknown suffix stripped")
	}
}

func TestSetModel_ClearsUnsupportedThinking(t *testing.T) {
	writePiModelFiles(t, nil, nil, map[string]any{"provider": map[string]any{"models": []any{
		map[string]any{"id": "reasoner", "reasoning": true, "thinkingLevelMap": map[string]any{"max": "max"}},
		map[string]any{"id": "basic-reasoner", "reasoning": true},
		map[string]any{"id": "chat"},
	}}})
	for _, tc := range []struct{ model, effort, want string }{
		{"provider/chat", "max", ""},
		{"provider/basic-reasoner", "max", ""},
		{"provider/basic-reasoner", "high", "high"},
		{"provider/chat", "off", "off"},
		{"provider/unknown", "max", "max"},
	} {
		t.Run(tc.model+"/"+tc.effort, func(t *testing.T) {
			a := &Agent{model: "provider/reasoner", thinking: tc.effort}
			a.SetModel(tc.model)
			if got := a.GetReasoningEffort(); got != tc.want {
				t.Fatalf("thinking = %q, want %q", got, tc.want)
			}
			session, err := a.StartSession(context.Background(), "")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := session.Close(); err != nil {
					t.Error(err)
				}
			})
			s := session.(*piSession)
			args := buildJSONArgs(nil, "hello", "", s.model, s.thinking, nil)
			if tc.want == "" && strings.Contains(strings.Join(args, " "), "--thinking") {
				t.Fatalf("stale thinking in launch args: %v", args)
			}
		})
	}
}

func TestAvailableModels_AmbiguousAliasesPreferDefaultProvider(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, preferred := range []string{"z-team", "", "missing"} {
			t.Run(fmt.Sprintf("enabled=%v/default=%s", enabled, preferred), func(t *testing.T) {
				settings := map[string]any{"defaultProvider": preferred}
				if enabled {
					settings["enabledModels"] = []string{"a-catalog/shared", "z-team/shared"}
				}
				writePiModelFiles(t, settings, nil, map[string]any{
					"a-catalog": map[string]any{"models": []any{map[string]any{"id": "shared"}}},
					"z-team":    map[string]any{"models": []any{map[string]any{"id": "shared"}}},
				})
				models := (&Agent{}).AvailableModels(context.Background())
				count := 0
				for _, m := range models {
					if m.Alias == "shared" {
						count++
						if m.Name != "z-team/shared" || preferred != "z-team" {
							t.Fatalf("wrong alias owner: %+v", m)
						}
					}
				}
				want := 0
				if preferred == "z-team" {
					want = 1
				}
				if count != want {
					t.Fatalf("alias owners = %d, want %d", count, want)
				}
				if len(models) != 2 {
					t.Fatalf("qualified options lost: %v", models)
				}
			})
		}
	}
}

func TestAvailableModels_DeduplicatesAndFiltersEmptyKeys(t *testing.T) {
	entry := map[string]any{"models": []any{map[string]any{"id": "same", "name": "first"}, map[string]any{"id": "same", "name": "last"}, map[string]any{"id": ""}, map[string]any{"id": " "}}}
	for _, source := range []string{"store", "custom", "both", "enabled"} {
		t.Run(source, func(t *testing.T) {
			providers := map[string]any{"p": entry, "": entry, " ": entry}
			var store, custom, settings any
			if source == "store" || source == "both" {
				store = providers
			}
			if source == "custom" || source == "both" {
				custom = map[string]any{"providers": providers}
			}
			if source == "enabled" {
				settings = map[string]any{"enabledModels": []string{"p/same", "p/same", "", "/bad", "p/"}}
			}
			writePiModelFiles(t, settings, custom, store)
			models := (&Agent{}).AvailableModels(context.Background())
			if len(models) != 1 || models[0].Name != "p/same" {
				t.Fatalf("invalid or duplicate models: %+v", models)
			}
			if source != "enabled" && models[0].Desc != "last" {
				t.Fatalf("last definition should win: %+v", models)
			}
		})
	}
}

func TestContextWindows_CatalogOnlyAndCustomOverride(t *testing.T) {
	writePiModelFiles(t, nil, map[string]any{"providers": map[string]any{"p": map[string]any{"models": []any{map[string]any{"id": "overridden", "contextWindow": 64000}}}}}, map[string]any{"p": map[string]any{"models": []any{map[string]any{"id": "catalog-only", "contextWindow": 128000}, map[string]any{"id": "overridden", "contextWindow": 32000}}}})
	windows := loadModelsContextWindows()
	for model, want := range map[string]int{"p/catalog-only": 128000, "catalog-only": 128000, "p/overridden": 64000, "overridden": 64000} {
		if windows[model] != want {
			t.Errorf("%s context window = %d, want %d", model, windows[model], want)
		}
	}
}

func TestAvailableModels_DisablesAmbiguousAliasesWithinDefaultProvider(t *testing.T) {
	writePiModelFiles(t, map[string]any{"defaultProvider": "p", "enabledModels": []string{"p/vendor-a/shared", "p/vendor-b/SHARED"}}, nil, nil)
	for _, m := range (&Agent{}).AvailableModels(context.Background()) {
		if m.Alias != "" {
			t.Fatalf("ambiguous alias retained: %+v", m)
		}
	}
}
