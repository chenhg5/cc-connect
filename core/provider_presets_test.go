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

func TestProviderPresetsIncludesUberLLM(t *testing.T) {
	body, err := os.ReadFile("../provider-presets.json")
	if err != nil {
		t.Fatalf("read provider-presets.json: %v", err)
	}

	var presets ProviderPresetsResponse
	if err := json.Unmarshal(body, &presets); err != nil {
		t.Fatalf("parse provider-presets.json: %v", err)
	}

	var uber *ProviderPreset
	for i := range presets.Providers {
		if presets.Providers[i].Name == "uberllm" {
			uber = &presets.Providers[i]
			break
		}
	}
	if uber == nil {
		t.Fatal("uberllm preset not found")
	}
	if uber.InviteURL != "" {
		t.Fatalf("uberllm invite_url = %q, want empty", uber.InviteURL)
	}

	claude := uber.AgentConfig("claudecode")
	if claude == nil {
		t.Fatal("uberllm claudecode config missing")
	}
	// Claude Code appends /v1/messages itself, so the base URL has no /v1.
	if claude.BaseURL != "https://api.uberllm.dev" {
		t.Fatalf("claudecode base_url = %q", claude.BaseURL)
	}
	if claude.Model != "anthropic/claude-sonnet-4.6" {
		t.Fatalf("claudecode model = %q", claude.Model)
	}

	codex := uber.AgentConfig("codex")
	if codex == nil {
		t.Fatal("uberllm codex config missing")
	}
	if codex.BaseURL != "https://api.uberllm.dev/v1" {
		t.Fatalf("codex base_url = %q", codex.BaseURL)
	}
	if codex.Model != "openai/gpt-5.4" {
		t.Fatalf("codex model = %q", codex.Model)
	}
	if codex.CodexConfig == nil || codex.CodexConfig.WireAPI != "responses" {
		t.Fatalf("codex_config = %#v", codex.CodexConfig)
	}

	opencode := uber.AgentConfig("opencode")
	if opencode == nil {
		t.Fatal("uberllm opencode config missing")
	}
	if opencode.BaseURL != "https://api.uberllm.dev/v1" {
		t.Fatalf("opencode base_url = %q", opencode.BaseURL)
	}
	if opencode.Model != "deepseek/deepseek-v4-flash" {
		t.Fatalf("opencode model = %q", opencode.Model)
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

	if !opper.SupportsAgent("opencode") {
		t.Fatal("opper should support opencode")
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

func TestProviderPresetsIncludesRequesty(t *testing.T) {
	body, err := os.ReadFile("../provider-presets.json")
	if err != nil {
		t.Fatalf("read provider-presets.json: %v", err)
	}

	var presets ProviderPresetsResponse
	if err := json.Unmarshal(body, &presets); err != nil {
		t.Fatalf("parse provider-presets.json: %v", err)
	}

	var requesty *ProviderPreset
	for i := range presets.Providers {
		if presets.Providers[i].Name == "requesty" {
			requesty = &presets.Providers[i]
			break
		}
	}
	if requesty == nil {
		t.Fatal("requesty preset not found")
	}
	if requesty.InviteURL != "" {
		t.Fatalf("requesty invite_url = %q, want empty", requesty.InviteURL)
	}

	claude := requesty.AgentConfig("claudecode")
	if claude == nil {
		t.Fatal("requesty claudecode config missing")
	}
	if claude.BaseURL != "https://router.requesty.ai" {
		t.Fatalf("claudecode base_url = %q", claude.BaseURL)
	}
	if claude.Model != "anthropic/claude-sonnet-5-5" {
		t.Fatalf("claudecode model = %q", claude.Model)
	}

	codex := requesty.AgentConfig("codex")
	if codex == nil {
		t.Fatal("requesty codex config missing")
	}
	if codex.BaseURL != "https://router.requesty.ai/v1" {
		t.Fatalf("codex base_url = %q", codex.BaseURL)
	}
	if codex.Model != "openai/gpt-5.4" {
		t.Fatalf("codex model = %q", codex.Model)
	}
	if codex.CodexConfig == nil || codex.CodexConfig.WireAPI != "responses" {
		t.Fatalf("codex_config = %#v", codex.CodexConfig)
	}

	opencode := requesty.AgentConfig("opencode")
	if opencode == nil {
		t.Fatal("requesty opencode config missing")
	}
	if opencode.BaseURL != "https://router.requesty.ai/v1" {
		t.Fatalf("opencode base_url = %q", opencode.BaseURL)
	}
}

func TestProviderPresetsIncludesTrustedRouter(t *testing.T) {
	body, err := os.ReadFile("../provider-presets.json")
	if err != nil {
		t.Fatalf("read provider-presets.json: %v", err)
	}

	var presets ProviderPresetsResponse
	if err := json.Unmarshal(body, &presets); err != nil {
		t.Fatalf("parse provider-presets.json: %v", err)
	}

	var trustedrouter *ProviderPreset
	for i := range presets.Providers {
		if presets.Providers[i].Name == "trustedrouter" {
			trustedrouter = &presets.Providers[i]
			break
		}
	}
	if trustedrouter == nil {
		t.Fatal("trustedrouter preset not found")
	}
	if trustedrouter.InviteURL != "" {
		t.Fatalf("trustedrouter invite_url = %q, want empty", trustedrouter.InviteURL)
	}

	claude := trustedrouter.AgentConfig("claudecode")
	if claude == nil {
		t.Fatal("trustedrouter claudecode config missing")
	}
	if claude.BaseURL != "https://api.trustedrouter.com" {
		t.Fatalf("claudecode base_url = %q", claude.BaseURL)
	}
	if claude.Model != "anthropic/claude-sonnet-4.6" {
		t.Fatalf("claudecode model = %q", claude.Model)
	}

	opencode := trustedrouter.AgentConfig("opencode")
	if opencode == nil {
		t.Fatal("trustedrouter opencode config missing")
	}
	if opencode.BaseURL != "https://api.trustedrouter.com/v1" {
		t.Fatalf("opencode base_url = %q", opencode.BaseURL)
	}
}

func TestProviderPresetsIncludesAIGROW(t *testing.T) {
	body, err := os.ReadFile("../provider-presets.json")
	if err != nil {
		t.Fatalf("read provider-presets.json: %v", err)
	}

	var presets ProviderPresetsResponse
	if err := json.Unmarshal(body, &presets); err != nil {
		t.Fatalf("parse provider-presets.json: %v", err)
	}

	var aigrow *ProviderPreset
	for i := range presets.Providers {
		if presets.Providers[i].Name == "aigrow" {
			aigrow = &presets.Providers[i]
			break
		}
	}
	if aigrow == nil {
		t.Fatal("aigrow preset not found")
	}
	if aigrow.InviteURL != "" {
		t.Fatalf("aigrow invite_url = %q, want empty", aigrow.InviteURL)
	}

	claude := aigrow.AgentConfig("claudecode")
	if claude == nil {
		t.Fatal("aigrow claudecode config missing")
	}
	if claude.BaseURL != "https://aigrow.gg" {
		t.Fatalf("claudecode base_url = %q", claude.BaseURL)
	}
	if claude.Model != "claude-opus-5.5" {
		t.Fatalf("claudecode model = %q", claude.Model)
	}

	opencode := aigrow.AgentConfig("opencode")
	if opencode == nil {
		t.Fatal("aigrow opencode config missing")
	}
	if opencode.BaseURL != "https://aigrow.gg/v1" {
		t.Fatalf("opencode base_url = %q", opencode.BaseURL)
	}
}

func TestProviderPresetsIncludesWorldRouter(t *testing.T) {
	body, err := os.ReadFile("../provider-presets.json")
	if err != nil {
		t.Fatalf("read provider-presets.json: %v", err)
	}

	var presets ProviderPresetsResponse
	if err := json.Unmarshal(body, &presets); err != nil {
		t.Fatalf("parse provider-presets.json: %v", err)
	}

	var worldrouter *ProviderPreset
	for i := range presets.Providers {
		if presets.Providers[i].Name == "worldrouter" {
			worldrouter = &presets.Providers[i]
			break
		}
	}
	if worldrouter == nil {
		t.Fatal("worldrouter preset not found")
	}
	if worldrouter.InviteURL != "" {
		t.Fatalf("worldrouter invite_url = %q, want empty", worldrouter.InviteURL)
	}

	claude := worldrouter.AgentConfig("claudecode")
	if claude == nil {
		t.Fatal("worldrouter claudecode config missing")
	}
	if claude.BaseURL != "https://inference-api.worldrouter.ai" {
		t.Fatalf("claudecode base_url = %q", claude.BaseURL)
	}
	if claude.Model != "claude-sonnet-5-5" {
		t.Fatalf("claudecode model = %q", claude.Model)
	}

	codex := worldrouter.AgentConfig("codex")
	if codex == nil {
		t.Fatal("worldrouter codex config missing")
	}
	if codex.BaseURL != "https://inference-api.worldrouter.ai/v1" {
		t.Fatalf("codex base_url = %q", codex.BaseURL)
	}
	if codex.CodexConfig == nil || codex.CodexConfig.WireAPI != "responses" {
		t.Fatalf("codex wire_api = %+v, want responses", codex.CodexConfig)
	}

	opencode := worldrouter.AgentConfig("opencode")
	if opencode == nil {
		t.Fatal("worldrouter opencode config missing")
	}
	if opencode.BaseURL != "https://inference-api.worldrouter.ai/v1" {
		t.Fatalf("opencode base_url = %q", opencode.BaseURL)
	}
}

func TestProviderPresetsIncludesLazu(t *testing.T) {
	body, err := os.ReadFile("../provider-presets.json")
	if err != nil {
		t.Fatalf("read provider-presets.json: %v", err)
	}

	var presets ProviderPresetsResponse
	if err := json.Unmarshal(body, &presets); err != nil {
		t.Fatalf("parse provider-presets.json: %v", err)
	}

	var lazu *ProviderPreset
	for i := range presets.Providers {
		if presets.Providers[i].Name == "lazu" {
			lazu = &presets.Providers[i]
			break
		}
	}
	if lazu == nil {
		t.Fatal("lazu preset not found")
	}
	if lazu.InviteURL != "" {
		t.Fatalf("lazu invite_url = %q, want empty", lazu.InviteURL)
	}

	claude := lazu.AgentConfig("claudecode")
	if claude == nil {
		t.Fatal("lazu claudecode config missing")
	}
	if claude.BaseURL != "https://api.lazu.ai" {
		t.Fatalf("claudecode base_url = %q", claude.BaseURL)
	}
	if claude.Model != "claude-sonnet-5-5" {
		t.Fatalf("claudecode model = %q", claude.Model)
	}

	codex := lazu.AgentConfig("codex")
	if codex == nil {
		t.Fatal("lazu codex config missing")
	}
	if codex.BaseURL != "https://api.lazu.ai/v1" {
		t.Fatalf("codex base_url = %q", codex.BaseURL)
	}
	if codex.Model != "gpt-6.2-astra" {
		t.Fatalf("codex model = %q", codex.Model)
	}
	if codex.CodexConfig == nil || codex.CodexConfig.WireAPI != "responses" {
		t.Fatalf("codex_config = %#v", codex.CodexConfig)
	}

	opencode := lazu.AgentConfig("opencode")
	if opencode == nil {
		t.Fatal("lazu opencode config missing")
	}
	if opencode.BaseURL != "https://api.lazu.ai/v1" {
		t.Fatalf("opencode base_url = %q", opencode.BaseURL)
	}
}

func TestProviderPresetsIncludesOnomeo(t *testing.T) {
	body, err := os.ReadFile("../provider-presets.json")
	if err != nil {
		t.Fatalf("read provider-presets.json: %v", err)
	}

	var presets ProviderPresetsResponse
	if err := json.Unmarshal(body, &presets); err != nil {
		t.Fatalf("parse provider-presets.json: %v", err)
	}

	var onomeo *ProviderPreset
	for i := range presets.Providers {
		if presets.Providers[i].Name == "onomeo" {
			onomeo = &presets.Providers[i]
			break
		}
	}
	if onomeo == nil {
		t.Fatal("onomeo preset not found")
	}
	if onomeo.InviteURL != "" {
		t.Fatalf("onomeo invite_url = %q, want empty", onomeo.InviteURL)
	}

	claude := onomeo.AgentConfig("claudecode")
	if claude == nil {
		t.Fatal("onomeo claudecode config missing")
	}
	if claude.BaseURL != "https://onomeo.com" {
		t.Fatalf("claudecode base_url = %q", claude.BaseURL)
	}
	if claude.Model != "claude-sonnet-5.5" {
		t.Fatalf("claudecode model = %q", claude.Model)
	}

	codex := onomeo.AgentConfig("codex")
	if codex == nil {
		t.Fatal("onomeo codex config missing")
	}
	if codex.BaseURL != "https://onomeo.com/v1" {
		t.Fatalf("codex base_url = %q", codex.BaseURL)
	}
	if codex.Model != "gpt-6.2-sol" {
		t.Fatalf("codex model = %q", codex.Model)
	}
	if codex.CodexConfig == nil || codex.CodexConfig.WireAPI != "responses" {
		t.Fatalf("codex_config = %#v", codex.CodexConfig)
	}

	opencode := onomeo.AgentConfig("opencode")
	if opencode == nil {
		t.Fatal("onomeo opencode config missing")
	}
	if opencode.BaseURL != "https://onomeo.com/v1" {
		t.Fatalf("opencode base_url = %q", opencode.BaseURL)
	}
	if opencode.Model != "deepseek-v4.1-flash" {
		t.Fatalf("opencode model = %q", opencode.Model)
	}
}

func TestProviderPresetsIncludesTeamoRouter(t *testing.T) {
	body, err := os.ReadFile("../provider-presets.json")
	if err != nil {
		t.Fatalf("read provider-presets.json: %v", err)
	}

	var presets ProviderPresetsResponse
	if err := json.Unmarshal(body, &presets); err != nil {
		t.Fatalf("parse provider-presets.json: %v", err)
	}

	var teamorouter *ProviderPreset
	for i := range presets.Providers {
		if presets.Providers[i].Name == "teamorouter" {
			teamorouter = &presets.Providers[i]
			break
		}
	}
	if teamorouter == nil {
		t.Fatal("teamorouter preset not found")
	}
	if teamorouter.InviteURL != "" {
		t.Fatalf("teamorouter invite_url = %q, want empty", teamorouter.InviteURL)
	}

	claude := teamorouter.AgentConfig("claudecode")
	if claude == nil {
		t.Fatal("teamorouter claudecode config missing")
	}
	if claude.BaseURL != "https://api.teamorouter.com" {
		t.Fatalf("claudecode base_url = %q", claude.BaseURL)
	}
	if claude.Model != "claude-sonnet-5-5" {
		t.Fatalf("claudecode model = %q", claude.Model)
	}

	codex := teamorouter.AgentConfig("codex")
	if codex == nil {
		t.Fatal("teamorouter codex config missing")
	}
	if codex.BaseURL != "https://api.teamorouter.com/v1" {
		t.Fatalf("codex base_url = %q", codex.BaseURL)
	}
	if codex.Model != "gpt-6.2-sol" {
		t.Fatalf("codex model = %q", codex.Model)
	}
	if codex.CodexConfig == nil || codex.CodexConfig.WireAPI != "responses" {
		t.Fatalf("codex_config = %#v", codex.CodexConfig)
	}

	opencode := teamorouter.AgentConfig("opencode")
	if opencode == nil {
		t.Fatal("teamorouter opencode config missing")
	}
	if opencode.BaseURL != "https://api.teamorouter.com/v1" {
		t.Fatalf("opencode base_url = %q", opencode.BaseURL)
	}
	if opencode.Model != "deepseek-v4-flash" {
		t.Fatalf("opencode model = %q", opencode.Model)
	}
}

func TestProviderPresetsIncludesStraitly(t *testing.T) {
	body, err := os.ReadFile("../provider-presets.json")
	if err != nil {
		t.Fatalf("read provider-presets.json: %v", err)
	}

	var presets ProviderPresetsResponse
	if err := json.Unmarshal(body, &presets); err != nil {
		t.Fatalf("parse provider-presets.json: %v", err)
	}

	var straitly *ProviderPreset
	for i := range presets.Providers {
		if presets.Providers[i].Name == "straitly" {
			straitly = &presets.Providers[i]
			break
		}
	}
	if straitly == nil {
		t.Fatal("straitly preset not found")
	}
	if straitly.InviteURL != "" {
		t.Fatalf("straitly invite_url = %q, want empty", straitly.InviteURL)
	}
	if straitly.Website != "https://straitly.ai" {
		t.Fatalf("straitly website = %q", straitly.Website)
	}

	claude := straitly.AgentConfig("claudecode")
	if claude == nil {
		t.Fatal("straitly claudecode config missing")
	}
	if claude.BaseURL != "https://api.straitly.ai" {
		t.Fatalf("claudecode base_url = %q", claude.BaseURL)
	}
	if claude.Model != "claude-sonnet-5-5" {
		t.Fatalf("claudecode model = %q", claude.Model)
	}

	codex := straitly.AgentConfig("codex")
	if codex == nil {
		t.Fatal("straitly codex config missing")
	}
	if codex.BaseURL != "https://api.straitly.ai/v1" {
		t.Fatalf("codex base_url = %q", codex.BaseURL)
	}
	if codex.Model != "gpt-6.1-sol" {
		t.Fatalf("codex model = %q", codex.Model)
	}
	if codex.CodexConfig == nil || codex.CodexConfig.WireAPI != "responses" {
		t.Fatalf("codex_config = %#v", codex.CodexConfig)
	}

	opencode := straitly.AgentConfig("opencode")
	if opencode == nil {
		t.Fatal("straitly opencode config missing")
	}
	if opencode.BaseURL != "https://api.straitly.ai/v1" {
		t.Fatalf("opencode base_url = %q", opencode.BaseURL)
	}
	if opencode.Model != "deepseek/deepseek-v4-1-flash-1017" {
		t.Fatalf("opencode model = %q", opencode.Model)
	}
}
