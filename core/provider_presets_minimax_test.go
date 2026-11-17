package core

import (
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestMiniMaxPresetsHTTPBoundary(t *testing.T) {
	body, err := os.ReadFile("../provider-presets.json")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/presets.json" {
			t.Errorf("unexpected catalog request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("public catalog request must not send credentials")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer server.Close()

	presets, err := fetchPresetsFromURL(server.URL+"/presets.json", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, provider := range presets.Providers {
		if provider.Name != "minimax" && provider.Name != "minimax-cn" {
			continue
		}
		found[provider.Name] = true
		if provider.Thinking != "" {
			t.Errorf("%s must not force a thinking override", provider.Name)
		}
		for _, agent := range []string{"claudecode", "codex", "opencode"} {
			cfg := provider.AgentConfig(agent)
			if cfg == nil {
				t.Fatalf("missing %s/%s preset", provider.Name, agent)
			}
			if cfg.Model != "MiniMax-M3.1-Flash" || !reflect.DeepEqual(cfg.Models, []string{"MiniMax-M3.1-Flash", "MiniMax-M3"}) {
				t.Errorf("%s/%s: unexpected default/catalog: %+v", provider.Name, agent, cfg)
			}
			if agent == "codex" && (cfg.CodexConfig == nil || cfg.CodexConfig.WireAPI != "responses") {
				t.Errorf("%s: Codex Responses handoff missing", provider.Name)
			}
		}
	}
	if len(found) != 2 {
		t.Fatalf("missing regional MiniMax presets: %v", found)
	}
}
