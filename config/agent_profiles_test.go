package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

const agentProfileFixture = `
[[providers]]
name = "shared"
api_key = "test-only"
agent_types = ["codex"]
[providers.agent_models]
codex = "profile-model"

[[agent_profiles]]
id = "planner-main"
name = "方案助手"
role = "planner"
prompt = "Use acceptance criteria from the specification."
[agent_profiles.agent]
type = "codex"
provider_refs = ["shared"]
[agent_profiles.agent.options]
system_prompt = "Keep the existing system prompt."
append_system_prompt = "Keep the existing extra instructions."
work_dir = "~/profile-work"

[[projects]]
name = "planning-bot"
agent_profile = "planner-main"
[projects.agent.options]
work_dir = "/runtime-work"
[[projects.platforms]]
type = "feishu"
`

func loadAgentProfileFixture(t *testing.T, data string) *Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestAgentProfile_ResolveAndSerializeWithoutFlattening(t *testing.T) {
	cfg := loadAgentProfileFixture(t, agentProfileFixture)
	for range 2 {
		proj, err := cfg.EffectiveProject(cfg.Projects[0])
		if err != nil {
			t.Fatal(err)
		}
		if proj.Agent.Type != "codex" || proj.Agent.Options["work_dir"] != "/runtime-work" {
			t.Fatalf("effective backend = %+v", proj.Agent)
		}
		prompt := proj.Agent.Options["append_system_prompt"].(string)
		for _, want := range []string{"Role: planner", "planning agent", "Use acceptance criteria", "Keep the existing extra"} {
			if !strings.Contains(prompt, want) {
				t.Fatalf("missing %q in prompt", want)
			}
		}
		if strings.Count(prompt, "Role: planner") != 1 {
			t.Fatal("role prompt duplicated")
		}
		if len(proj.Agent.Providers) != 1 || proj.Agent.Providers[0].Model != "profile-model" {
			t.Fatalf("profile provider refs not resolved: %+v", proj.Agent.Providers)
		}
		proj.Agent.Options["system_prompt"] = "mutated runtime"
	}
	if cfg.Projects[0].Agent.Type != "" || cfg.AgentProfiles[0].Agent.Options["system_prompt"] != "Keep the existing system prompt." {
		t.Fatal("resolving a profile mutated the raw configuration")
	}
	var saved bytes.Buffer
	if err := toml.NewEncoder(&saved).Encode(cfg); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(saved.String(), "You are the planning agent") {
		t.Fatal("generated role prompt leaked into persisted config")
	}
	reloaded := loadAgentProfileFixture(t, saved.String())
	if _, err := reloaded.EffectiveProject(reloaded.Projects[0]); err != nil {
		t.Fatal(err)
	}
}

func TestAgentProfile_Validation(t *testing.T) {
	for _, tc := range []struct{ name, from, to, want string }{
		{"unknown reference", `agent_profile = "planner-main"`, `agent_profile = "missing"`, "unknown agent profile"},
		{"unknown role", `role = "planner"`, `role = "invented"`, "unknown agent role"},
		{"unsupported backend", `type = "codex"`, `type = "other"`, "supported backend"},
		{"invalid id", `id = "planner-main"`, `id = "../planner"`, "invalid agent profile"},
		{"prompt type", `system_prompt = "Keep the existing system prompt."`, `system_prompt = 42`, "must be a string"},
		{"backend conflict", `[projects.agent.options]`, "[projects.agent]\ntype = \"acp\"\n[projects.agent.options]", "backend conflicts"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(p, []byte(strings.Replace(agentProfileFixture, tc.from, tc.to, 1)), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(p)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
	cfg := loadAgentProfileFixture(t, agentProfileFixture)
	cfg.AgentProfiles = append(cfg.AgentProfiles, cfg.AgentProfiles[0])
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "duplicate agent profile") {
		t.Fatalf("duplicate profile accepted: %v", err)
	}
}

func TestAgentProfile_MultiWorkspaceChecksInheritedWorkDir(t *testing.T) {
	cfg := loadAgentProfileFixture(t, agentProfileFixture)
	cfg.Projects[0].Mode, cfg.Projects[0].BaseDir = "multi-workspace", "/workspaces"
	delete(cfg.Projects[0].Agent.Options, "work_dir")
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("inherited work_dir bypassed validation: %v", err)
	}
	delete(cfg.AgentProfiles[0].Agent.Options, "work_dir")
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
}

func TestAgentProfile_LegacyConfigUnchanged(t *testing.T) {
	cfg := &Config{}
	proj := ProjectConfig{Name: "legacy", Agent: AgentConfig{Type: "codex", Options: map[string]any{"append_system_prompt": "original"}}}
	got, err := cfg.EffectiveProject(proj)
	if err != nil || got.Agent.Options["append_system_prompt"] != "original" {
		t.Fatalf("legacy changed: %+v, %v", got, err)
	}
}
