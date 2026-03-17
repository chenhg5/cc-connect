package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{
			name:    "requires at least one project",
			cfg:     Config{},
			wantErr: "at least one [[projects]] entry is required",
		},
		{
			name: "requires project name",
			cfg: Config{
				Projects: []ProjectConfig{
					validProject(""),
				},
			},
			wantErr: `projects[0].name is required`,
		},
		{
			name: "requires agent type",
			cfg: Config{
				Projects: []ProjectConfig{
					func() ProjectConfig {
						p := validProject("demo")
						p.Agent.Type = ""
						return p
					}(),
				},
			},
			wantErr: `projects[0].agent.type is required`,
		},
		{
			name: "requires at least one platform",
			cfg: Config{
				Projects: []ProjectConfig{
					func() ProjectConfig {
						p := validProject("demo")
						p.Platforms = nil
						return p
					}(),
				},
			},
			wantErr: `projects[0] needs at least one [[projects.platforms]]`,
		},
		{
			name: "requires platform type",
			cfg: Config{
				Projects: []ProjectConfig{
					func() ProjectConfig {
						p := validProject("demo")
						p.Platforms[0].Type = ""
						return p
					}(),
				},
			},
			wantErr: `projects[0].platforms[0].type is required`,
		},
		{
			name: "multi workspace requires base dir",
			cfg: Config{
				Projects: []ProjectConfig{
					func() ProjectConfig {
						p := validProject("demo")
						p.Mode = "multi-workspace"
						return p
					}(),
				},
			},
			wantErr: `project "demo": multi-workspace mode requires base_dir`,
		},
		{
			name: "multi workspace rejects work dir",
			cfg: Config{
				Projects: []ProjectConfig{
					func() ProjectConfig {
						p := validProject("demo")
						p.Mode = "multi-workspace"
						p.BaseDir = "~/workspace"
						p.Agent.Options["work_dir"] = "/tmp/demo"
						return p
					}(),
				},
			},
			wantErr: `project "demo": multi-workspace mode conflicts with agent work_dir`,
		},
		{
			name: "accepts valid config",
			cfg: Config{
				Projects: []ProjectConfig{validProject("demo")},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validate() unexpected error: %v", err)
				}
				return
			}
			assertErrContains(t, err, tt.wantErr)
		})
	}
}

func TestLoad_DefaultsDataDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(baseConfigTOML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	want := filepath.Join(dir, ".cc-connect")
	if cfg.DataDir != want {
		t.Fatalf("Load() data_dir = %q, want %q", cfg.DataDir, want)
	}
}

func TestListProjects(t *testing.T) {
	writeTestConfig(t, baseConfigTOML)

	names, err := ListProjects()
	if err != nil {
		t.Fatalf("ListProjects() error: %v", err)
	}
	if len(names) != 1 || names[0] != "demo" {
		t.Fatalf("ListProjects() = %#v, want [demo]", names)
	}
}

func TestSaveLanguage(t *testing.T) {
	writeTestConfig(t, baseConfigTOML)

	if err := SaveLanguage("zh"); err != nil {
		t.Fatalf("SaveLanguage() error: %v", err)
	}

	cfg := readTestConfig(t)
	if cfg.Language != "zh" {
		t.Fatalf("Language = %q, want zh", cfg.Language)
	}
}

func TestProviderConfig_SaveActiveProviderAndGetProjectProviders(t *testing.T) {
	writeTestConfig(t, providerConfigTOML)

	if err := SaveActiveProvider("demo", "backup"); err != nil {
		t.Fatalf("SaveActiveProvider() error: %v", err)
	}

	providers, active, err := GetProjectProviders("demo")
	if err != nil {
		t.Fatalf("GetProjectProviders() error: %v", err)
	}
	if active != "backup" {
		t.Fatalf("active provider = %q, want backup", active)
	}
	if len(providers) != 2 {
		t.Fatalf("provider count = %d, want 2", len(providers))
	}
}

func TestProviderConfig_AddAndRemove(t *testing.T) {
	writeTestConfig(t, providerConfigTOML)

	newProvider := ProviderConfig{Name: "relay", APIKey: "sk-relay", BaseURL: "https://example.com"}
	if err := AddProviderToConfig("demo", newProvider); err != nil {
		t.Fatalf("AddProviderToConfig() error: %v", err)
	}
	if err := AddProviderToConfig("demo", newProvider); err == nil {
		t.Fatal("AddProviderToConfig() duplicate provider: expected error")
	}

	cfg := readTestConfig(t)
	if len(cfg.Projects[0].Agent.Providers) != 3 {
		t.Fatalf("provider count after add = %d, want 3", len(cfg.Projects[0].Agent.Providers))
	}

	if err := RemoveProviderFromConfig("demo", "relay"); err != nil {
		t.Fatalf("RemoveProviderFromConfig() error: %v", err)
	}
	if err := RemoveProviderFromConfig("demo", "relay"); err == nil {
		t.Fatal("RemoveProviderFromConfig() missing provider: expected error")
	}
}

func TestCommandConfig_AddAndRemove(t *testing.T) {
	writeTestConfig(t, baseConfigTOML)

	cmd := CommandConfig{Name: "review", Description: "code review", Prompt: "review {{args}}"}
	if err := AddCommand(cmd); err != nil {
		t.Fatalf("AddCommand() error: %v", err)
	}
	if err := AddCommand(cmd); err == nil {
		t.Fatal("AddCommand() duplicate command: expected error")
	}

	cfg := readTestConfig(t)
	if len(cfg.Commands) != 1 || cfg.Commands[0].Name != "review" {
		t.Fatalf("commands after add = %#v, want one review command", cfg.Commands)
	}

	if err := RemoveCommand("review"); err != nil {
		t.Fatalf("RemoveCommand() error: %v", err)
	}
	if err := RemoveCommand("review"); err == nil {
		t.Fatal("RemoveCommand() missing command: expected error")
	}
}

func TestAliasConfig_AddAndRemove(t *testing.T) {
	writeTestConfig(t, baseConfigTOML)

	if err := AddAlias(AliasConfig{Name: "帮助", Command: "/help"}); err != nil {
		t.Fatalf("AddAlias() error: %v", err)
	}
	if err := AddAlias(AliasConfig{Name: "帮助", Command: "/list"}); err != nil {
		t.Fatalf("AddAlias() update error: %v", err)
	}

	cfg := readTestConfig(t)
	if len(cfg.Aliases) != 1 || cfg.Aliases[0].Command != "/list" {
		t.Fatalf("aliases after update = %#v, want one updated alias", cfg.Aliases)
	}

	if err := RemoveAlias("帮助"); err != nil {
		t.Fatalf("RemoveAlias() error: %v", err)
	}
	if err := RemoveAlias("帮助"); err == nil {
		t.Fatal("RemoveAlias() missing alias: expected error")
	}
}

func TestDisplayConfig_Save(t *testing.T) {
	writeTestConfig(t, baseConfigTOML)

	thinking := 120
	tool := 240
	if err := SaveDisplayConfig(&thinking, &tool); err != nil {
		t.Fatalf("SaveDisplayConfig() error: %v", err)
	}

	cfg := readTestConfig(t)
	if cfg.Display.ThinkingMaxLen == nil || *cfg.Display.ThinkingMaxLen != 120 {
		t.Fatalf("ThinkingMaxLen = %#v, want 120", cfg.Display.ThinkingMaxLen)
	}
	if cfg.Display.ToolMaxLen == nil || *cfg.Display.ToolMaxLen != 240 {
		t.Fatalf("ToolMaxLen = %#v, want 240", cfg.Display.ToolMaxLen)
	}

	thinking = 360
	if err := SaveDisplayConfig(&thinking, nil); err != nil {
		t.Fatalf("SaveDisplayConfig() second update error: %v", err)
	}

	cfg = readTestConfig(t)
	if cfg.Display.ThinkingMaxLen == nil || *cfg.Display.ThinkingMaxLen != 360 {
		t.Fatalf("ThinkingMaxLen after update = %#v, want 360", cfg.Display.ThinkingMaxLen)
	}
	if cfg.Display.ToolMaxLen == nil || *cfg.Display.ToolMaxLen != 240 {
		t.Fatalf("ToolMaxLen after nil update = %#v, want 240", cfg.Display.ToolMaxLen)
	}
}

func TestTTSConfig_SaveMode(t *testing.T) {
	writeTestConfig(t, baseConfigTOML)

	if err := SaveTTSMode("always"); err != nil {
		t.Fatalf("SaveTTSMode() error: %v", err)
	}

	cfg := readTestConfig(t)
	if cfg.TTS.TTSMode != "always" {
		t.Fatalf("TTSMode = %q, want always", cfg.TTS.TTSMode)
	}
}

func TestSaveFeishuPlatformCredentials_UpdateFirstCandidateAndAllowFrom(t *testing.T) {
	configPath := writeConfigFixture(t, feishuConfigFixture)
	patchConfigPath(t, configPath)

	result, err := SaveFeishuPlatformCredentials(FeishuCredentialUpdateOptions{
		ProjectName:       "alpha",
		AppID:             "cli_new_app",
		AppSecret:         "sec_new_secret",
		OwnerOpenID:       "ou_new_owner",
		SetAllowFromEmpty: true,
	})
	if err != nil {
		t.Fatalf("SaveFeishuPlatformCredentials returned error: %v", err)
	}

	if result.ProjectName != "alpha" {
		t.Fatalf("result.ProjectName = %q, want %q", result.ProjectName, "alpha")
	}
	if result.PlatformAbsIndex != 1 {
		t.Fatalf("result.PlatformAbsIndex = %d, want 1", result.PlatformAbsIndex)
	}
	if result.AllowFrom != "ou_new_owner" {
		t.Fatalf("result.AllowFrom = %q, want %q", result.AllowFrom, "ou_new_owner")
	}

	cfg := readConfigFixture(t, configPath)
	platform := cfg.Projects[0].Platforms[1]
	if platform.Type != "feishu" {
		t.Fatalf("platform.Type = %q, want %q", platform.Type, "feishu")
	}
	if got := stringMapValue(platform.Options, "app_id"); got != "cli_new_app" {
		t.Fatalf("app_id = %q, want %q", got, "cli_new_app")
	}
	if got := stringMapValue(platform.Options, "app_secret"); got != "sec_new_secret" {
		t.Fatalf("app_secret = %q, want %q", got, "sec_new_secret")
	}
	if got := stringMapValue(platform.Options, "allow_from"); got != "ou_new_owner" {
		t.Fatalf("allow_from = %q, want %q", got, "ou_new_owner")
	}
}

func TestSaveFeishuPlatformCredentials_SelectByIndexAndOverrideType(t *testing.T) {
	configPath := writeConfigFixture(t, feishuConfigFixture)
	patchConfigPath(t, configPath)

	result, err := SaveFeishuPlatformCredentials(FeishuCredentialUpdateOptions{
		ProjectName:       "alpha",
		PlatformIndex:     2,
		PlatformType:      "feishu",
		AppID:             "cli_second_app",
		AppSecret:         "sec_second_secret",
		OwnerOpenID:       "ou_should_not_override",
		SetAllowFromEmpty: true,
	})
	if err != nil {
		t.Fatalf("SaveFeishuPlatformCredentials returned error: %v", err)
	}

	if result.PlatformAbsIndex != 2 {
		t.Fatalf("result.PlatformAbsIndex = %d, want 2", result.PlatformAbsIndex)
	}
	if result.PlatformType != "feishu" {
		t.Fatalf("result.PlatformType = %q, want %q", result.PlatformType, "feishu")
	}
	if result.AllowFrom != "ou_existing_owner" {
		t.Fatalf("result.AllowFrom = %q, want %q", result.AllowFrom, "ou_existing_owner")
	}

	cfg := readConfigFixture(t, configPath)
	platform := cfg.Projects[0].Platforms[2]
	if platform.Type != "feishu" {
		t.Fatalf("platform.Type = %q, want %q", platform.Type, "feishu")
	}
	if got := stringMapValue(platform.Options, "app_id"); got != "cli_second_app" {
		t.Fatalf("app_id = %q, want %q", got, "cli_second_app")
	}
	if got := stringMapValue(platform.Options, "app_secret"); got != "sec_second_secret" {
		t.Fatalf("app_secret = %q, want %q", got, "sec_second_secret")
	}
	if got := stringMapValue(platform.Options, "allow_from"); got != "ou_existing_owner" {
		t.Fatalf("allow_from = %q, want %q", got, "ou_existing_owner")
	}
}

func TestSaveFeishuPlatformCredentials_ReturnsIndexRangeError(t *testing.T) {
	configPath := writeConfigFixture(t, feishuConfigFixture)
	patchConfigPath(t, configPath)

	_, err := SaveFeishuPlatformCredentials(FeishuCredentialUpdateOptions{
		ProjectName:   "alpha",
		PlatformIndex: 3,
		AppID:         "cli_any",
		AppSecret:     "sec_any",
	})
	if err == nil {
		t.Fatal("expected error for out-of-range platform index, got nil")
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Fatalf("error = %q, want contains %q", err.Error(), "out of range")
	}
}

func TestEnsureProjectWithFeishuPlatform_CreatesMissingProject(t *testing.T) {
	configPath := writeConfigFixture(t, feishuConfigFixture)
	patchConfigPath(t, configPath)

	result, err := EnsureProjectWithFeishuPlatform(EnsureProjectWithFeishuOptions{
		ProjectName:  "gamma",
		PlatformType: "lark",
		WorkDir:      "/tmp/gamma",
	})
	if err != nil {
		t.Fatalf("EnsureProjectWithFeishuPlatform returned error: %v", err)
	}
	if !result.Created {
		t.Fatal("result.Created = false, want true")
	}
	if result.AddedPlatform {
		t.Fatal("result.AddedPlatform = true, want false")
	}

	cfg := readConfigFixture(t, configPath)
	if len(cfg.Projects) != 2 {
		t.Fatalf("len(cfg.Projects) = %d, want 2", len(cfg.Projects))
	}
	proj := cfg.Projects[1]
	if proj.Name != "gamma" {
		t.Fatalf("proj.Name = %q, want %q", proj.Name, "gamma")
	}
	if len(proj.Platforms) != 1 {
		t.Fatalf("len(proj.Platforms) = %d, want 1", len(proj.Platforms))
	}
	if proj.Platforms[0].Type != "lark" {
		t.Fatalf("platform type = %q, want %q", proj.Platforms[0].Type, "lark")
	}
	if got := stringMapValue(proj.Agent.Options, "work_dir"); got != "/tmp/gamma" {
		t.Fatalf("work_dir = %q, want explicit override %q", got, "/tmp/gamma")
	}
}

func TestEnsureProjectWithFeishuPlatform_AddsPlatformWhenProjectExistsWithoutFeishu(t *testing.T) {
	configPath := writeConfigFixture(t, projectWithoutFeishuFixture)
	patchConfigPath(t, configPath)

	result, err := EnsureProjectWithFeishuPlatform(EnsureProjectWithFeishuOptions{
		ProjectName:  "beta",
		PlatformType: "feishu",
	})
	if err != nil {
		t.Fatalf("EnsureProjectWithFeishuPlatform returned error: %v", err)
	}
	if result.Created {
		t.Fatal("result.Created = true, want false")
	}
	if !result.AddedPlatform {
		t.Fatal("result.AddedPlatform = false, want true")
	}

	cfg := readConfigFixture(t, configPath)
	proj := cfg.Projects[0]
	if len(proj.Platforms) != 2 {
		t.Fatalf("len(proj.Platforms) = %d, want 2", len(proj.Platforms))
	}
	if proj.Platforms[1].Type != "feishu" {
		t.Fatalf("platform type = %q, want %q", proj.Platforms[1].Type, "feishu")
	}
}

func TestSaveFeishuPlatformCredentials_PreservesCommentsAndUnknownFields(t *testing.T) {
	configPath := writeConfigFixture(t, preserveFormatFixture)
	patchConfigPath(t, configPath)

	_, err := SaveFeishuPlatformCredentials(FeishuCredentialUpdateOptions{
		ProjectName: "alpha",
		AppID:       "cli_new_app",
		AppSecret:   "sec_new_secret",
	})
	if err != nil {
		t.Fatalf("SaveFeishuPlatformCredentials returned error: %v", err)
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config fixture: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "# top comment should stay") {
		t.Fatalf("expected top comment to be preserved, got:\n%s", text)
	}
	if !strings.Contains(text, `custom_top = "keep_me"`) {
		t.Fatalf("expected unknown top-level field to be preserved, got:\n%s", text)
	}
	if !strings.Contains(text, `custom_option = "still_here"`) {
		t.Fatalf("expected unknown options field to be preserved, got:\n%s", text)
	}
	if !strings.Contains(text, "keep inline comment") {
		t.Fatalf("expected inline comment to be preserved, got:\n%s", text)
	}
}

func validProject(name string) ProjectConfig {
	return ProjectConfig{
		Name: name,
		Agent: AgentConfig{
			Type:    "claudecode",
			Options: map[string]any{"mode": "default"},
		},
		Platforms: []PlatformConfig{
			{Type: "telegram", Options: map[string]any{"token": "test-token"}},
		},
	}
}

func assertErrContains(t *testing.T, err error, want string) {
	t.Helper()

	if err == nil {
		t.Fatalf("expected error containing %q, got nil", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want substring %q", err.Error(), want)
	}
}

func writeTestConfig(t *testing.T, content string) {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	oldPath := ConfigPath
	ConfigPath = path
	t.Cleanup(func() {
		ConfigPath = oldPath
	})
}

func readTestConfig(t *testing.T) Config {
	t.Helper()

	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	return cfg
}

func writeConfigFixture(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config fixture: %v", err)
	}
	return path
}

func patchConfigPath(t *testing.T, path string) {
	t.Helper()
	prev := ConfigPath
	ConfigPath = path
	t.Cleanup(func() {
		ConfigPath = prev
	})
}

func readConfigFixture(t *testing.T, path string) *Config {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config fixture: %v", err)
	}
	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		t.Fatalf("parse config fixture: %v", err)
	}
	return cfg
}

func stringMapValue(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

const baseConfigTOML = `
[[projects]]
name = "demo"

[projects.agent]
type = "claudecode"

[projects.agent.options]
mode = "default"

[[projects.platforms]]
type = "telegram"

[projects.platforms.options]
token = "test-token"
`

const providerConfigTOML = `
[[projects]]
name = "demo"

[projects.agent]
type = "claudecode"

[projects.agent.options]
mode = "default"
provider = "primary"

[[projects.agent.providers]]
name = "primary"
api_key = "sk-primary"

[[projects.agent.providers]]
name = "backup"
api_key = "sk-backup"

[[projects.platforms]]
type = "telegram"

[projects.platforms.options]
token = "test-token"
`

const feishuConfigFixture = `
[[projects]]
name = "alpha"

[projects.agent]
type = "codex"

[projects.agent.options]
work_dir = "/tmp/alpha"

[[projects.platforms]]
type = "telegram"

[projects.platforms.options]
bot_token = "token_xxx"

[[projects.platforms]]
type = "feishu"

[projects.platforms.options]
app_id = "old_feishu_app"
app_secret = "old_feishu_secret"

[[projects.platforms]]
type = "lark"

[projects.platforms.options]
app_id = "old_lark_app"
app_secret = "old_lark_secret"
allow_from = "ou_existing_owner"
`

const projectWithoutFeishuFixture = `
[[projects]]
name = "beta"

[projects.agent]
type = "codex"

[projects.agent.options]
work_dir = "/tmp/beta"

[[projects.platforms]]
type = "telegram"

[projects.platforms.options]
bot_token = "token_xxx"
`

const preserveFormatFixture = `# top comment should stay
custom_top = "keep_me"

[[projects]]
name = "alpha"

[projects.agent]
type = "codex"

[projects.agent.options]
work_dir = "/tmp/alpha"

[[projects.platforms]]
type = "feishu"

[projects.platforms.options]
app_id = "old_app" # keep inline comment
app_secret = "old_secret"
custom_option = "still_here"
`
