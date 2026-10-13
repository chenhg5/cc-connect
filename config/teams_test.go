package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestConfigureTeamBot_NewAndSwitchedMembersUseInteractiveApprovals(t *testing.T) {
	old := ConfigPath
	defer func() { ConfigPath = old }()
	for _, backend := range []string{"codex", "claudecode", "trae"} {
		for _, same := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/inherited=%v", backend, same), func(t *testing.T) {
				ConfigPath = filepath.Join(t.TempDir(), "config.toml")
				typ := backend
				if typ == "trae" {
					typ = "acp"
				}
				if !same {
					typ = "other"
				}
				raw := fmt.Sprintf(`[[projects]]
name="boss"
[projects.agent]
type="codex"
[projects.agent.options]
backend="exec"
mode="yolo"
[[projects]]
name="dev"
[projects.agent]
type=%q
[projects.agent.options]
work_dir="/tmp"
model="inherited-model"
backend="exec"
mode="bypassPermissions"
cmd="traecli"
args=["--permission-mode", "bypass_permissions", "acp", "serve", "--custom"]
[[teams]]
id="default"
manager="boss"
[[teams.members]]
bot="boss"
role="manager"
`, typ)
				if err := os.WriteFile(ConfigPath, []byte(raw), 0600); err != nil {
					t.Fatal(err)
				}
				if err := ConfigureTeamBot("boss", "dev", "developer", backend, ""); err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(ConfigPath)
				if err != nil {
					t.Fatal(err)
				}
				var cfg Config
				if err := toml.Unmarshal(data, &cfg); err != nil {
					t.Fatal(err)
				}
				opts := cfg.Projects[1].Agent.Options
				if opts["work_dir"] != "/tmp" {
					t.Fatal("workspace lost")
				}
				if same && opts["model"] != "inherited-model" {
					t.Fatal("same backend lost model")
				}
				if !same && opts["model"] != nil {
					t.Fatal("foreign model inherited")
				}
				if cfg.Projects[0].Agent.Options["mode"] != "yolo" {
					t.Fatal("parent mutated")
				}
				switch backend {
				case "codex":
					if opts["backend"] != "app_server" || opts["mode"] != "suggest" {
						t.Fatalf("not interactive: %v", opts)
					}
				case "claudecode":
					if opts["mode"] != "default" {
						t.Fatalf("not interactive: %v", opts)
					}
				case "trae":
					want := "[--permission-mode default acp serve]"
					if same {
						want = "[--permission-mode default acp serve --custom]"
					}
					if fmt.Sprint(opts["args"]) != want || opts["mode"] != nil {
						t.Fatalf("not interactive: %v", opts)
					}
				}
			})
		}
	}
}

func TestTeamConfiguration_PreservesNativeOptionsAndSeparatesRoleBackend(t *testing.T) {
	c := Config{Projects: []ProjectConfig{{Name: "boss", Mode: "multi-workspace", BaseDir: "/tmp", Agent: AgentConfig{Type: "codex", Options: map[string]any{"model": "m1", "append_system_prompt": "local"}}}, {Name: "dev", Agent: AgentConfig{Type: "codex"}}}, Teams: []TeamConfig{{ID: "t", Manager: "boss", Members: []TeamMember{{Bot: "boss", Role: "manager", Prompt: "custom"}, {Bot: "dev", Role: "developer"}}}}}
	if err := c.validateTeams(); err != nil {
		t.Fatal(err)
	}
	p, err := c.EffectiveProject(c.Projects[0])
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "" || p.Mode != "multi-workspace" || p.Agent.Options["model"] != "m1" {
		t.Fatal("team replaced native runtime")
	}
	prompt := p.Agent.Options["append_system_prompt"].(string)
	if !strings.Contains(prompt, "custom") || !strings.Contains(prompt, "local") {
		t.Fatal("prompt missing")
	}
	if c.Projects[0].Agent.Options["append_system_prompt"] != "local" {
		t.Fatal("effective prompt polluted source")
	}
	c.Teams[0].Manager = "unknown"
	if c.validateTeams() == nil {
		t.Fatal("accepted missing manager")
	}
}
func TestConfigureTeamBot_PersistsOneSourceAndDropsForeignProviderOnBackendChange(t *testing.T) {
	old := ConfigPath
	defer func() { ConfigPath = old }()
	ConfigPath = filepath.Join(t.TempDir(), "config.toml")
	raw := `[[projects]]
name="boss"
[projects.agent]
type="codex"
[projects.agent.options]
work_dir="/tmp"
[[projects]]
name="dev"
[projects.agent]
type="codex"
provider_refs=["private-provider"]
[projects.agent.options]
work_dir="/tmp"
secret="parent-private"
[[teams]]
id="default"
manager="boss"
[[teams.members]]
bot="boss"
role="manager"
`
	if err := os.WriteFile(ConfigPath, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ConfigureTeamBot("boss", "dev", "reviewer", "trae", "review only"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(ConfigPath)
	if strings.Contains(string(data), "parent-private") || strings.Contains(string(data), "private-provider") {
		t.Fatal("backend inherited foreign settings")
	}
	if !strings.Contains(string(data), "review only") || !strings.Contains(string(data), "traecli") {
		t.Fatal("new role/backend missing")
	}
}

func TestConfigureTeamBot_SameTraeBackendPreservesOptionsAndVerifiedName(t *testing.T) {
	old := ConfigPath
	defer func() { ConfigPath = old }()
	ConfigPath = filepath.Join(t.TempDir(), "config.toml")
	raw := `[[projects]]
name="boss"
[projects.agent]
type="codex"
[[projects.platforms]]
type="feishu"
[[projects]]
name="dev"
[projects.agent]
type="acp"
[projects.agent.options]
cmd="traecli"
model="keep-this-model"
args=["acp","serve","--custom"]
[[projects.platforms]]
type="feishu"
[[teams]]
id="default"
manager="boss"
[[teams.members]]
bot="boss"
role="manager"
[[teams.members]]
bot="dev"
display_name="审查助手"
role="developer"
`
	if err := os.WriteFile(ConfigPath, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ConfigureTeamBot("boss", "dev", "reviewer", "trae", "Updated instructions"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Projects[1].Agent.Options["model"] != "keep-this-model" || cfg.Teams[0].Members[1].DisplayName != "审查助手" {
		t.Fatal("same-backend identity edit reset execution options or verified name")
	}
}
