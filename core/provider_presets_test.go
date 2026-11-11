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

func TestProviderPresetsIncludesCloudGPU(t *testing.T) {
	body, err := os.ReadFile("../provider-presets.json")
	if err != nil {
		t.Fatalf("read provider-presets.json: %v", err)
	}

	var presets ProviderPresetsResponse
	if err := json.Unmarshal(body, &presets); err != nil {
		t.Fatalf("parse provider-presets.json: %v", err)
	}

	var cloud *ProviderPreset
	for i := range presets.Providers {
		if presets.Providers[i].Name == "cloudgpu" {
			cloud = &presets.Providers[i]
			break
		}
	}
	if cloud == nil {
		t.Fatal("cloudgpu preset not found")
	}
	if cloud.InviteURL != "" {
		t.Fatalf("cloudgpu invite_url = %q, want empty", cloud.InviteURL)
	}

	codex := cloud.AgentConfig("codex")
	if codex == nil {
		t.Fatal("cloudgpu codex config missing")
	}
	if codex.BaseURL != "https://cloudgpu.app/v1" {
		t.Fatalf("codex base_url = %q", codex.BaseURL)
	}
	if codex.Model != "deepseek-v4-flash" {
		t.Fatalf("codex model = %q", codex.Model)
	}
	if codex.CodexConfig == nil || codex.CodexConfig.WireAPI != "chat" {
		t.Fatalf("codex_config = %#v", codex.CodexConfig)
	}
	if !cloud.SupportsAgent("opencode") {
		t.Fatal("cloudgpu should support opencode")
	}
}

func TestProviderPresetsIncludesStandardCompute(t *testing.T) {
	body, err := os.ReadFile("../provider-presets.json")
	if err != nil {
		t.Fatalf("read provider-presets.json: %v", err)
	}

	var presets ProviderPresetsResponse
	if err := json.Unmarshal(body, &presets); err != nil {
		t.Fatalf("parse provider-presets.json: %v", err)
	}

	var sc *ProviderPreset
	for i := range presets.Providers {
		if presets.Providers[i].Name == "standardcompute" {
			sc = &presets.Providers[i]
			break
		}
	}
	if sc == nil {
		t.Fatal("standardcompute preset not found")
	}
	if sc.InviteURL != "" {
		t.Fatalf("standardcompute invite_url = %q, want empty", sc.InviteURL)
	}

	codex := sc.AgentConfig("codex")
	if codex == nil {
		t.Fatal("standardcompute codex config missing")
	}
	if codex.BaseURL != "https://api.stdcmpt.com/v1" {
		t.Fatalf("codex base_url = %q", codex.BaseURL)
	}
	if codex.Model != "StandardCompute" {
		t.Fatalf("codex model = %q", codex.Model)
	}
	if codex.CodexConfig == nil || codex.CodexConfig.WireAPI != "responses" {
		t.Fatalf("codex_config = %#v", codex.CodexConfig)
	}
	if !sc.SupportsAgent("opencode") {
		t.Fatal("standardcompute should support opencode")
	}
}
