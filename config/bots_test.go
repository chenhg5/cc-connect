package config

import (
	"bytes"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/chenhg5/cc-connect/core"
)

func TestBots_BackendBelongsToBotAndRawConfigIsPreserved(t *testing.T) {
	cfg := &Config{Bots: []BotConfig{
		{ID: "one", Name: "One", Role: "developer", Agent: AgentConfig{Type: "codex"}},
		{ID: "two", Role: "developer", Agent: AgentConfig{Type: "claudecode"}},
		{ID: "three", Role: "reviewer", Agent: AgentConfig{Type: "codex"}},
	}}
	if err := cfg.validatePermissive(); err != nil {
		t.Fatal(err)
	}
	for _, p := range cfg.RuntimeProjects() {
		got, err := cfg.EffectiveProject(p)
		if err != nil {
			t.Fatal(err)
		}
		if got.Agent.Type != p.Agent.Type || !strings.Contains(got.Agent.Options["append_system_prompt"].(string), "Role: "+p.Role) {
			t.Fatal("incorrect bot resolution")
		}
		got.Agent.Options["model"] = "changed"
	}
	var out bytes.Buffer
	if err := toml.NewEncoder(&out).Encode(cfg); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Role:") || strings.Contains(out.String(), "changed") || strings.Contains(out.String(), "[[projects]]") {
		t.Fatal("runtime projection leaked into source")
	}
	if cfg.Bots[0].Name != "One" || cfg.RuntimeProjects()[0].Name != "one" {
		t.Fatal("stable ID depends on display name")
	}
}

func TestBots_ProjectMembershipAndDuplicateIDs(t *testing.T) {
	bot := BotConfig{ID: "dev", Role: "developer", Agent: AgentConfig{Type: "codex"}}
	project := core.BusinessProject{ID: "app", BotIDs: []string{"dev"}, PrimaryDirectory: "src", Directories: []core.ProjectDirectory{{ID: "src", Path: t.TempDir()}}}
	for _, test := range []struct {
		name   string
		change func(*Config)
	}{
		{"duplicate bot", func(c *Config) { c.Bots = append(c.Bots, bot) }},
		{"unknown role", func(c *Config) { c.Bots[0].Role = "unknown" }},
		{"legacy profile", func(c *Config) { c.Bots[0].AgentProfile = "old" }},
		{"unknown bot", func(c *Config) { c.BusinessProjects[0].BotIDs = []string{"missing"} }},
		{"bad primary", func(c *Config) { c.BusinessProjects[0].PrimaryDirectory = "missing" }},
		{"duplicate project", func(c *Config) { c.BusinessProjects = append(c.BusinessProjects, project) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := Config{Bots: []BotConfig{bot}, BusinessProjects: []core.BusinessProject{project}}
			test.change(&c)
			if c.validatePermissive() == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func TestBots_MigrateSharedProfileWithoutSharingBackendState(t *testing.T) {
	cfg := loadAgentProfileFixture(t, agentProfileFixture)
	other := cfg.Projects[0]
	other.Name = "planning-other"
	cfg.Projects = append(cfg.Projects, other)
	migrated, err := cfg.MigrateAgentProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrated.Bots) != 2 || len(migrated.BusinessProjects) != 0 || len(migrated.AgentProfiles) != 0 {
		t.Fatal("incorrect migration")
	}
	for _, b := range migrated.Bots {
		if strings.Contains(b.Agent.Options["append_system_prompt"].(string), "Role:") {
			t.Fatal("generated prompt persisted")
		}
	}
	migrated.Bots[0].Agent.Options["model"] = "changed"
	if migrated.Bots[1].Agent.Options["model"] == "changed" || cfg.Projects[0].Agent.Options["model"] == "changed" {
		t.Fatal("backend state shared")
	}
	if cfg.Projects[0].AgentProfile == "" {
		t.Fatal("source mutated")
	}
}

func TestTeams_ExampleLoadsOrdinaryRuntimesAndExpandsWorkDir(t *testing.T) {
	t.Setenv("FEISHU_OWNER_OPEN_ID", "ou_example_owner")
	t.Setenv("FEISHU_APP_ID", "cli_example")
	t.Setenv("FEISHU_APP_SECRET", "example-secret")
	cfg, err := Load("../config.agent-team.example.toml")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Projects) != 1 || len(cfg.Teams) != 1 || len(cfg.Bots) != 0 || len(cfg.BusinessProjects) != 0 {
		t.Fatal("example did not load ordinary runtime plus optional Team")
	}
	if strings.HasPrefix(cfg.Projects[0].Agent.Options["work_dir"].(string), "~") {
		t.Fatal("project path was not expanded")
	}
	if _, err := cfg.EffectiveProject(cfg.RuntimeProjects()[0]); err != nil {
		t.Fatal(err)
	}
}
