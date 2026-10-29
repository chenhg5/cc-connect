package core

import (
	"encoding/json"
	"os"
	"testing"
)

func TestProviderPresetsIncludesAtlasCloud(t *testing.T) {
	body, err := os.ReadFile("../provider-presets.json")
	if err != nil {
		t.Fatalf("read provider-presets.json: %v", err)
	}

	var presets ProviderPresetsResponse
	if err := json.Unmarshal(body, &presets); err != nil {
		t.Fatalf("parse provider-presets.json: %v", err)
	}

	var atlas *ProviderPreset
	for i := range presets.Providers {
		if presets.Providers[i].Name == "atlascloud" {
			atlas = &presets.Providers[i]
			break
		}
	}
	if atlas == nil {
		t.Fatal("atlascloud preset not found")
	}
	if atlas.InviteURL != "" {
		t.Fatalf("atlascloud invite_url = %q, want empty", atlas.InviteURL)
	}

	codex := atlas.AgentConfig("codex")
	if codex == nil {
		t.Fatal("atlascloud codex config missing")
	}
	if codex.BaseURL != "https://api.atlascloud.ai/v1" {
		t.Fatalf("codex base_url = %q", codex.BaseURL)
	}
	if codex.Model != "qwen/qwen3.5-flash" {
		t.Fatalf("codex model = %q", codex.Model)
	}
	if codex.CodexConfig == nil || codex.CodexConfig.WireAPI != "chat" {
		t.Fatalf("codex_config = %#v", codex.CodexConfig)
	}
	if !atlas.SupportsAgent("opencode") {
		t.Fatal("atlascloud should support opencode")
	}
}

func TestProviderPresetsIncludesOpper(t *testing.T) {
	body, err := os.ReadFile("../provider-presets.json")
	if err != nil {
		t.Fatalf("read provider-presets.json: %v", err)
	}

	var presets ProviderPresetsResponse
	if err := json.Unmarshal(body, &presets); err != nil {
		t.Fatalf("parse provider-presets.json: %v", err)
	}

	var opper *ProviderPreset
	for i := range presets.Providers {
		if presets.Providers[i].Name == "opper" {
			opper = &presets.Providers[i]
			break
		}
	}
	if opper == nil {
		t.Fatal("opper preset not found")
	}
	if opper.InviteURL != "" {
		t.Fatalf("opper invite_url = %q, want empty", opper.InviteURL)
	}

	claude := opper.AgentConfig("claudecode")
	if claude == nil {
		t.Fatal("opper claudecode config missing")
	}
	if claude.BaseURL != "https://api.opper.ai/v3/compat" {
		t.Fatalf("claudecode base_url = %q", claude.BaseURL)
	}
	if claude.Model != "claude-sonnet-4-6" {
		t.Fatalf("claudecode model = %q", claude.Model)
	}

	codex := opper.AgentConfig("codex")
	if codex == nil {
		t.Fatal("opper codex config missing")
	}
	if codex.BaseURL != "https://api.opper.ai/v3/compat" {
		t.Fatalf("codex base_url = %q", codex.BaseURL)
	}
	if codex.Model != "gpt-5.5" {
		t.Fatalf("codex model = %q", codex.Model)
	}
	if codex.CodexConfig == nil || codex.CodexConfig.WireAPI != "responses" {
		t.Fatalf("codex_config = %#v", codex.CodexConfig)
	}
	if !opper.SupportsAgent("opencode") {
		t.Fatal("opper should support opencode")
	}
}
