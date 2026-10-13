package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestBotIdentity_CloneAndClaimPreserveParent(t *testing.T) {
	old := ConfigPath
	ConfigPath = filepath.Join(t.TempDir(), "config.toml")
	t.Cleanup(func() { ConfigPath = old })
	input := `# keep this comment
language = "zh"
[[projects]]
name = "supervisor"
mode = "multi-workspace"
base_dir = "/tmp/workspaces"
admin_from = "ou_parent_owner"
run_as_user = "worker"
[projects.agent]
type = "acp"
provider_refs = ["shared"]
[projects.agent.options]
cmd = "/custom/trae"
args = ["acp", "serve"]
work_dir = "/tmp/project"
[projects.agent.options.env]
CUSTOM = "value"
[[projects.platforms]]
type = "feishu"
[projects.platforms.options]
app_id = "cli_parent"
app_secret = "parent-secret"
allow_from = "ou_parent_owner"
`
	if err := os.WriteFile(ConfigPath, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("ab", 32)
	if err := AddBotIdentity("supervisor", "developer", "feishu", "cli_child", "child-secret", hash); err != nil {
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
	if len(cfg.Projects) != 2 {
		t.Fatalf("projects=%d", len(cfg.Projects))
	}
	p := cfg.Projects[1]
	if p.Platforms[0].Options["single_card_reply"] != true || p.Platforms[0].Options["separate_final_card"] != true {
		t.Fatal("new bot must use process and final answer cards")
	}
	if p.Agent.Options["cmd"] != "/custom/trae" || len(p.Agent.ProviderRefs) != 1 || p.Mode != "multi-workspace" || p.BaseDir != "/tmp/workspaces" || p.RunAsUser != "worker" {
		t.Fatalf("agent/workspace settings lost: %+v", p.Agent)
	}
	if p.AdminFrom != "" || p.BotOwnerID != "" || p.BotOwnerClaimHash != hash || p.Platforms[0].Options["app_id"] != "cli_child" {
		t.Fatal("child ownership/platform incorrectly inherited")
	}
	if strings.Contains(string(data)[strings.Index(string(data), `name = "developer"`):], "parent-secret") {
		t.Fatal("inherited parent secret")
	}
	before := string(data)
	if err := AddBotIdentity("supervisor", "developer", "feishu", "cli_other", "other", hash); err == nil {
		t.Fatal("duplicate overwrote bot")
	}
	if err := ClaimBotIdentity("developer", "wrong-hash", "ou_child_owner"); err == nil {
		t.Fatal("wrong claim accepted")
	}
	data, _ = os.ReadFile(ConfigPath)
	if string(data) != before {
		t.Fatal("rejected operation changed configuration")
	}
	if err := ClaimBotIdentity("developer", hash, "ou_child_owner"); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(ConfigPath)
	if err := toml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Projects[1].AdminFrom != "ou_child_owner" || cfg.Projects[1].BotOwnerClaimHash != "" || cfg.Projects[0].AdminFrom != "ou_parent_owner" {
		t.Fatal("owner binding changed wrong project or failed to consume code")
	}
	if !strings.Contains(string(data), "# keep this comment") {
		t.Fatal("lost existing comments")
	}
	if err := ClaimBotIdentity("developer", hash, "attacker"); err == nil {
		t.Fatal("claim was reusable")
	}
	info, _ := os.Stat(ConfigPath)
	if info.Mode().Perm() != 0600 {
		t.Fatal("configuration is not private")
	}
}

func TestBotIdentity_RejectsInvalidInputBeforeWrite(t *testing.T) {
	for _, name := range []string{"../escape", "bad/name", "", "UPPER"} {
		if err := AddBotIdentity("parent", name, "feishu", "id", "secret", strings.Repeat("ab", 32)); err == nil {
			t.Fatalf("accepted name %q", name)
		}
	}
}

func TestBotIdentity_V2CreatesBotWithoutBusinessProjectAndClaimsOwnIdentity(t *testing.T) {
	old := ConfigPath
	ConfigPath = filepath.Join(t.TempDir(), "config.toml")
	t.Cleanup(func() { ConfigPath = old })
	data := `# keep comment
[[bots]]
id = "boss"
name = "Manager display name"
role = "manager"
[bots.agent]
type = "codex"
[[bots.platforms]]
type = "feishu"
[bots.platforms.options]
app_id = "cli_boss"
app_secret = "boss-test-only"
`
	if err := os.WriteFile(ConfigPath, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("ab", 32)
	if err := AddBotIdentity("boss", "developer", "feishu", "cli_dev", "test-only", hash); err != nil {
		t.Fatal(err)
	}
	if err := ClaimBotIdentity("developer", hash, "ou_child_owner"); err != nil {
		t.Fatal(err)
	}
	saved, _ := os.ReadFile(ConfigPath)
	var c Config
	if err := toml.Unmarshal(saved, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Bots) != 2 || len(c.Projects) != 0 || len(c.BusinessProjects) != 0 {
		t.Fatal("Bot creation produced a business project or legacy runtime")
	}
	if c.Bots[1].ID != "developer" || c.Bots[1].BotOwnerID != "ou_child_owner" || c.Bots[0].BotOwnerID != "" {
		t.Fatal("identity claim crossed bot boundary")
	}
	if !strings.Contains(string(saved), "# keep comment") {
		t.Fatal("source comment lost")
	}
}

func TestBotIdentity_AutomaticOwnerIsSavedAtomicallyWithoutParentIdentity(t *testing.T) {
	old := ConfigPath
	ConfigPath = filepath.Join(t.TempDir(), "config.toml")
	t.Cleanup(func() { ConfigPath = old })
	input := `# preserve parent
[[bots]]
id = "manager"
name = "Manager"
role = "manager"
admin_from = "ou_parent"
bot_owner_id = "ou_parent"
[bots.agent]
type = "codex"
[[bots.platforms]]
type = "feishu"
[bots.platforms.options]
app_id = "cli_parent"
app_secret = "parent-secret"
allow_from = "ou_parent"
`
	if err := os.WriteFile(ConfigPath, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"", "*", "ou_a,ou_b", "ou_a\n"} {
		if err := AddOwnedBotIdentity("manager", "developer", "feishu", "cli_child", "child-secret", invalid); err == nil {
			t.Fatal("accepted invalid owner")
		}
		got, _ := os.ReadFile(ConfigPath)
		if string(got) != input {
			t.Fatal("failed binding changed config")
		}
	}
	if err := AddOwnedBotIdentity("manager", "developer", "feishu", "cli_child", "child-secret", "ou_scanner"); err != nil {
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
	var before Config
	if err := toml.Unmarshal([]byte(input), &before); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "# preserve parent") || !reflect.DeepEqual(cfg.Bots[0], before.Bots[0]) {
		t.Fatal("parent config changed")
	}
	b := cfg.Bots[1]
	if b.BotOwnerID != "ou_scanner" || b.AdminFrom != "ou_scanner" || b.BotOwnerClaimHash != "" || b.Users != nil || b.Platforms[0].Options["allow_from"] != nil {
		t.Fatal("automatic ownership not isolated")
	}
	if err := ClaimBotIdentity("developer", strings.Repeat("ab", 32), "ou_attacker"); err == nil {
		t.Fatal("auto-bound owner replaced by claim")
	}
	if err := AddOwnedBotIdentity("manager", "developer", "feishu", "cli_other", "other", "ou_other"); err == nil {
		t.Fatal("existing bot replaced")
	}
	if err := AddOwnedBotIdentity("manager", "duplicate-app", "feishu", "cli_child", "other", "ou_other"); err == nil {
		t.Fatal("same app configured twice")
	}
	after, _ := os.ReadFile(ConfigPath)
	if string(after) != string(data) {
		t.Fatal("rejected operation changed config")
	}
}
