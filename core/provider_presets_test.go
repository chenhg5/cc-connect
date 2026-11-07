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

func TestProviderPresetsIncludesRuncrate(t *testing.T) {
	body, err := os.ReadFile("../provider-presets.json")
	if err != nil {
		t.Fatalf("read provider-presets.json: %v", err)
	}

	var presets ProviderPresetsResponse
	if err := json.Unmarshal(body, &presets); err != nil {
		t.Fatalf("parse provider-presets.json: %v", err)
	}

	var runcrate *ProviderPreset
	for i := range presets.Providers {
		if presets.Providers[i].Name == "runcrate" {
			runcrate = &presets.Providers[i]
			break
		}
	}
	if runcrate == nil {
		t.Fatal("runcrate preset not found")
	}
	if runcrate.InviteURL != "" {
		t.Fatalf("runcrate invite_url = %q, want empty", runcrate.InviteURL)
	}

	claude := runcrate.AgentConfig("claudecode")
	if claude == nil {
		t.Fatal("runcrate claudecode config missing")
	}
	if claude.BaseURL != "https://api.runcrate.ai" {
		t.Fatalf("claudecode base_url = %q", claude.BaseURL)
	}

	codex := runcrate.AgentConfig("codex")
	if codex == nil {
		t.Fatal("runcrate codex config missing")
	}
	if codex.BaseURL != "https://api.runcrate.ai/v1" {
		t.Fatalf("codex base_url = %q", codex.BaseURL)
	}
	if codex.Model != "deepseek-ai/DeepSeek-V4.1-Flash" {
		t.Fatalf("codex model = %q", codex.Model)
	}
	if codex.CodexConfig == nil || codex.CodexConfig.WireAPI != "responses" {
		t.Fatalf("codex_config = %#v", codex.CodexConfig)
	}

	opencode := runcrate.AgentConfig("opencode")
	if opencode == nil {
		t.Fatal("runcrate opencode config missing")
	}
	if opencode.BaseURL != "https://api.runcrate.ai/v1" {
		t.Fatalf("opencode base_url = %q", opencode.BaseURL)
	}
}
