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
		{
			name: "accepts valid references config",
			cfg: Config{
				Projects: []ProjectConfig{
					func() ProjectConfig {
						p := validProject("demo")
						p.References = ReferenceConfig{
							NormalizeAgents: []string{"codex", "claudecode"},
							RenderPlatforms: []string{"feishu", "weixin"},
							DisplayPath:     "dirname_basename",
							MarkerStyle:     "emoji",
							EnclosureStyle:  "code",
						}
						return p
					}(),
				},
			},
		},
		{
			name: "rejects unsupported reference agent",
			cfg: Config{
				Projects: []ProjectConfig{
					func() ProjectConfig {
						p := validProject("demo")
						p.References.NormalizeAgents = []string{"gemini"}
						return p
					}(),
				},
			},
			wantErr: `projects[0].references.normalize_agents has unsupported value "gemini"`,
		},
		{
			name: "rejects unsupported reference platform",
			cfg: Config{
				Projects: []ProjectConfig{
					func() ProjectConfig {
						p := validProject("demo")
						p.References.RenderPlatforms = []string{"telegram"}
						return p
					}(),
				},
			},
			wantErr: `projects[0].references.render_platforms has unsupported value "telegram"`,
		},
		{
			name: "rejects unsupported reference display path",
			cfg: Config{
				Projects: []ProjectConfig{
					func() ProjectConfig {
						p := validProject("demo")
						p.References.DisplayPath = "full"
						return p
					}(),
				},
			},
			wantErr: `projects[0].references.display_path has unsupported value "full"`,
		},
		{
			name: "accepts all shorthand in references scopes",
			cfg: Config{
				Projects: []ProjectConfig{
					func() ProjectConfig {
						p := validProject("demo")
						p.References.NormalizeAgents = []string{"all"}
						p.References.RenderPlatforms = []string{"all"}
						return p
					}(),
				},
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

func TestEffectiveDisplayQuiet(t *testing.T) {
	tru, fal := true, false
	tests := []struct {
		name     string
		cfg      Config
		proj     ProjectConfig
		wantTM   bool
		wantTool bool
	}{
		{
			name:     "defaults no quiet",
			cfg:      Config{},
			proj:     ProjectConfig{},
			wantTM:   true,
			wantTool: true,
		},
		{
			name:     "global quiet maps when display unset",
			cfg:      Config{Quiet: &tru},
			proj:     ProjectConfig{},
			wantTM:   false,
			wantTool: false,
		},
		{
			name:     "project quiet maps when display unset",
			cfg:      Config{},
			proj:     ProjectConfig{Quiet: &tru},
			wantTM:   false,
			wantTool: false,
		},
		{
			name: "explicit thinking_messages wins over quiet",
			cfg: Config{
				Quiet:   &tru,
				Display: DisplayConfig{ThinkingMessages: &tru},
			},
			proj:     ProjectConfig{},
			wantTM:   true,
			wantTool: false,
		},
		{
			name:     "project quiet false overrides global quiet",
			cfg:      Config{Quiet: &tru},
			proj:     ProjectConfig{Quiet: &fal},
			wantTM:   true,
			wantTool: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tm, tool, _, _ := EffectiveDisplay(&tt.cfg, &tt.proj)
			if tm != tt.wantTM {
				t.Fatalf("ThinkingMessages = %v, want %v", tm, tt.wantTM)
			}
			if tool != tt.wantTool {
				t.Fatalf("ToolMessages = %v, want %v", tool, tt.wantTool)
			}
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

func TestProviderConfig_SaveProviderModel(t *testing.T) {
	writeTestConfig(t, providerConfigTOML)

	if err := SaveProviderModel("demo", "primary", "gpt-5.4"); err != nil {
		t.Fatalf("SaveProviderModel() error: %v", err)
	}

	cfg := readTestConfig(t)
	if got := cfg.Projects[0].Agent.Providers[0].Model; got != "gpt-5.4" {
		t.Fatalf("provider model = %q, want gpt-5.4", got)
	}
	if err := SaveProviderModel("demo", "missing", "gpt-4.1"); err == nil {
		t.Fatal("SaveProviderModel() missing provider: expected error")
	}
}

func TestSaveAgentModel(t *testing.T) {
	writeTestConfig(t, providerConfigTOML)

	if err := SaveAgentModel("demo", "gpt-5.4"); err != nil {
		t.Fatalf("SaveAgentModel() error: %v", err)
	}

	cfg := readTestConfig(t)
	if got, _ := cfg.Projects[0].Agent.Options["model"].(string); got != "gpt-5.4" {
		t.Fatalf("agent.options.model = %q, want gpt-5.4", got)
	}
	if got, _ := cfg.Projects[0].Agent.Options["mode"].(string); got != "default" {
		t.Fatalf("agent.options.mode = %q, want default", got)
	}
	if got, _ := cfg.Projects[0].Agent.Options["provider"].(string); got != "primary" {
		t.Fatalf("agent.options.provider = %q, want primary", got)
	}
	if len(cfg.Projects[0].Agent.Providers) != 2 {
		t.Fatalf("provider count = %d, want 2", len(cfg.Projects[0].Agent.Providers))
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
	showTools := false
	if err := SaveDisplayConfig(nil, &thinking, &tool, &showTools); err != nil {
		t.Fatalf("SaveDisplayConfig() error: %v", err)
	}

	cfg := readTestConfig(t)
	if cfg.Display.ThinkingMaxLen == nil || *cfg.Display.ThinkingMaxLen != 120 {
		t.Fatalf("ThinkingMaxLen = %#v, want 120", cfg.Display.ThinkingMaxLen)
	}
	if cfg.Display.ToolMaxLen == nil || *cfg.Display.ToolMaxLen != 240 {
		t.Fatalf("ToolMaxLen = %#v, want 240", cfg.Display.ToolMaxLen)
	}
	if cfg.Display.ToolMessages == nil || *cfg.Display.ToolMessages {
		t.Fatalf("ToolMessages = %#v, want false", cfg.Display.ToolMessages)
	}

	thinking = 360
	if err := SaveDisplayConfig(nil, &thinking, nil, nil); err != nil {
		t.Fatalf("SaveDisplayConfig() second update error: %v", err)
	}

	cfg = readTestConfig(t)
	if cfg.Display.ThinkingMaxLen == nil || *cfg.Display.ThinkingMaxLen != 360 {
		t.Fatalf("ThinkingMaxLen after update = %#v, want 360", cfg.Display.ThinkingMaxLen)
	}
	if cfg.Display.ToolMaxLen == nil || *cfg.Display.ToolMaxLen != 240 {
		t.Fatalf("ToolMaxLen after nil update = %#v, want 240", cfg.Display.ToolMaxLen)
	}
	if cfg.Display.ToolMessages == nil || *cfg.Display.ToolMessages {
		t.Fatalf("ToolMessages after nil update = %#v, want false", cfg.Display.ToolMessages)
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

const attachmentSendConfigFixture = `
attachment_send = "off"

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
`

const relayConfigFixture = `
[relay]
timeout_secs = 300

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
`

const relayConfigNegativeFixture = `
[relay]
timeout_secs = -1

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
`

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
	if result.AllowFrom != "ou_existing_owner,ou_should_not_override" {
		t.Fatalf("result.AllowFrom = %q, want %q", result.AllowFrom, "ou_existing_owner,ou_should_not_override")
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
	if got := stringMapValue(platform.Options, "allow_from"); got != "ou_existing_owner,ou_should_not_override" {
		t.Fatalf("allow_from = %q, want %q", got, "ou_existing_owner,ou_should_not_override")
	}
}

func TestSaveFeishuPlatformCredentials_AppendsOwnerToAllowFrom(t *testing.T) {
	configPath := writeConfigFixture(t, feishuConfigFixture)
	patchConfigPath(t, configPath)

	result, err := SaveFeishuPlatformCredentials(FeishuCredentialUpdateOptions{
		ProjectName:       "alpha",
		PlatformIndex:     2,
		PlatformType:      "feishu",
		AppID:             "cli_second_app",
		AppSecret:         "sec_second_secret",
		OwnerOpenID:       "ou_new_owner",
		SetAllowFromEmpty: true,
	})
	if err != nil {
		t.Fatalf("SaveFeishuPlatformCredentials returned error: %v", err)
	}

	if result.AllowFrom != "ou_existing_owner,ou_new_owner" {
		t.Fatalf("result.AllowFrom = %q, want %q", result.AllowFrom, "ou_existing_owner,ou_new_owner")
	}

	cfg := readConfigFixture(t, configPath)
	platform := cfg.Projects[0].Platforms[2]
	if got := stringMapValue(platform.Options, "allow_from"); got != "ou_existing_owner,ou_new_owner" {
		t.Fatalf("allow_from = %q, want %q", got, "ou_existing_owner,ou_new_owner")
	}
}

func TestSaveFeishuPlatformCredentials_LeavesWildcardAllowFromUnchanged(t *testing.T) {
	configPath := writeConfigFixture(t, strings.Replace(feishuConfigFixture, `allow_from = "ou_existing_owner"`, `allow_from = "*"`, 1))
	patchConfigPath(t, configPath)

	result, err := SaveFeishuPlatformCredentials(FeishuCredentialUpdateOptions{
		ProjectName:       "alpha",
		PlatformIndex:     2,
		OwnerOpenID:       "ou_new_owner",
		AppID:             "cli_second_app",
		AppSecret:         "sec_second_secret",
		SetAllowFromEmpty: true,
	})
	if err != nil {
		t.Fatalf("SaveFeishuPlatformCredentials returned error: %v", err)
	}

	if result.AllowFrom != "*" {
		t.Fatalf("result.AllowFrom = %q, want %q", result.AllowFrom, "*")
	}

	cfg := readConfigFixture(t, configPath)
	platform := cfg.Projects[0].Platforms[2]
	if got := stringMapValue(platform.Options, "allow_from"); got != "*" {
		t.Fatalf("allow_from = %q, want %q", got, "*")
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

func TestLoad_DefaultsAttachmentSendToOn(t *testing.T) {
	configPath := writeConfigFixture(t, projectWithoutFeishuFixture)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.AttachmentSend != "on" {
		t.Fatalf("cfg.AttachmentSend = %q, want %q", cfg.AttachmentSend, "on")
	}
}

func TestLoad_DefaultsAutoCompressDisabled(t *testing.T) {
	configPath := writeConfigFixture(t, projectWithoutFeishuFixture)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if len(cfg.Projects) == 0 {
		t.Fatalf("expected at least one project")
	}
	if cfg.Projects[0].AutoCompress.Enabled != nil {
		t.Fatalf("expected auto_compress.enabled to default to nil")
	}
}

func TestLoad_ParsesResetOnIdleMins(t *testing.T) {
	configPath := writeConfigFixture(t, projectWithResetOnIdleFixture)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Projects[0].ResetOnIdleMins == nil {
		t.Fatal("expected reset_on_idle_mins to be parsed")
	}
	if got := *cfg.Projects[0].ResetOnIdleMins; got != 60 {
		t.Fatalf("reset_on_idle_mins = %d, want 60", got)
	}
}

func TestLoad_RejectsNegativeResetOnIdleMins(t *testing.T) {
	configPath := writeConfigFixture(t, projectWithNegativeResetOnIdleFixture)

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("expected error for negative reset_on_idle_mins")
	}
	if !strings.Contains(err.Error(), "reset_on_idle_mins") {
		t.Fatalf("error = %q, want reset_on_idle_mins validation", err.Error())
	}
}

func TestLoad_ParsesAttachmentSendOff(t *testing.T) {
	configPath := writeConfigFixture(t, attachmentSendConfigFixture)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.AttachmentSend != "off" {
		t.Fatalf("cfg.AttachmentSend = %q, want %q", cfg.AttachmentSend, "off")
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

func TestLoadRelayTimeoutConfig(t *testing.T) {
	configPath := writeConfigFixture(t, relayConfigFixture)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Relay.TimeoutSecs == nil {
		t.Fatal("cfg.Relay.TimeoutSecs = nil, want non-nil")
	}
	if *cfg.Relay.TimeoutSecs != 300 {
		t.Fatalf("cfg.Relay.TimeoutSecs = %d, want 300", *cfg.Relay.TimeoutSecs)
	}
}

func TestLoadRejectsNegativeRelayTimeout(t *testing.T) {
	configPath := writeConfigFixture(t, relayConfigNegativeFixture)

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("expected error for negative relay timeout, got nil")
	}
	if !strings.Contains(err.Error(), "relay.timeout_secs must be >= 0") {
		t.Fatalf("error = %q, want contains %q", err.Error(), "relay.timeout_secs must be >= 0")
	}
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

const projectWithResetOnIdleFixture = `
[[projects]]
name = "beta"
reset_on_idle_mins = 60

[projects.agent]
type = "codex"

[projects.agent.options]
work_dir = "/tmp/beta"

[[projects.platforms]]
type = "telegram"

[projects.platforms.options]
bot_token = "token_xxx"
`

const projectWithNegativeResetOnIdleFixture = `
[[projects]]
name = "beta"
reset_on_idle_mins = -1

[projects.agent]
type = "codex"

[projects.agent.options]
work_dir = "/tmp/beta"

[[projects.platforms]]
type = "telegram"

[projects.platforms.options]
bot_token = "token_xxx"
`

const weixinConfigFixture = `
[[projects]]
name = "alpha"

[projects.agent]
type = "codex"

[projects.agent.options]
work_dir = "/tmp/alpha"

[[projects.platforms]]
type = "weixin"

[projects.platforms.options]
token = "old_weixin_token"
base_url = "https://ilink.example"
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

// --- validateUsersConfig tests ---

func TestValidateUsersConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{
			name: "nil users is valid",
			cfg: Config{
				Projects: []ProjectConfig{{
					Name:      "p1",
					Agent:     AgentConfig{Type: "codex"},
					Platforms: []PlatformConfig{{Type: "telegram", Options: map[string]any{"token": "x"}}},
					Users:     nil,
				}},
			},
			wantErr: "",
		},
		{
			name: "empty roles",
			cfg: Config{
				Projects: []ProjectConfig{{
					Name:      "p1",
					Agent:     AgentConfig{Type: "codex"},
					Platforms: []PlatformConfig{{Type: "telegram", Options: map[string]any{"token": "x"}}},
					Users:     &UsersConfig{Roles: map[string]RoleConfig{}},
				}},
			},
			wantErr: `no roles defined`,
		},
		{
			name: "empty user_ids in role",
			cfg: Config{
				Projects: []ProjectConfig{{
					Name:      "p1",
					Agent:     AgentConfig{Type: "codex"},
					Platforms: []PlatformConfig{{Type: "telegram", Options: map[string]any{"token": "x"}}},
					Users: &UsersConfig{
						Roles: map[string]RoleConfig{
							"admin": {UserIDs: []string{}},
						},
					},
				}},
			},
			wantErr: `empty user_ids`,
		},
		{
			name: "duplicate user in different roles",
			cfg: Config{
				Projects: []ProjectConfig{{
					Name:      "p1",
					Agent:     AgentConfig{Type: "codex"},
					Platforms: []PlatformConfig{{Type: "telegram", Options: map[string]any{"token": "x"}}},
					Users: &UsersConfig{
						Roles: map[string]RoleConfig{
							"admin":  {UserIDs: []string{"user1"}},
							"member": {UserIDs: []string{"user1"}},
						},
					},
				}},
			},
			wantErr: `appears in both role`,
		},
		{
			name: "wildcard in multiple roles",
			cfg: Config{
				Projects: []ProjectConfig{{
					Name:      "p1",
					Agent:     AgentConfig{Type: "codex"},
					Platforms: []PlatformConfig{{Type: "telegram", Options: map[string]any{"token": "x"}}},
					Users: &UsersConfig{
						Roles: map[string]RoleConfig{
							"admin":  {UserIDs: []string{"*"}},
							"member": {UserIDs: []string{"*"}},
						},
					},
				}},
			},
			wantErr: `wildcard`,
		},
		{
			name: "default_role not matching any role",
			cfg: Config{
				Projects: []ProjectConfig{{
					Name:      "p1",
					Agent:     AgentConfig{Type: "codex"},
					Platforms: []PlatformConfig{{Type: "telegram", Options: map[string]any{"token": "x"}}},
					Users: &UsersConfig{
						DefaultRole: "superadmin",
						Roles: map[string]RoleConfig{
							"admin": {UserIDs: []string{"u1"}},
						},
					},
				}},
			},
			wantErr: `default_role`,
		},
		{
			name: "valid users config",
			cfg: Config{
				Projects: []ProjectConfig{{
					Name:      "p1",
					Agent:     AgentConfig{Type: "codex"},
					Platforms: []PlatformConfig{{Type: "telegram", Options: map[string]any{"token": "x"}}},
					Users: &UsersConfig{
						DefaultRole: "member",
						Roles: map[string]RoleConfig{
							"admin":  {UserIDs: []string{"admin1"}},
							"member": {UserIDs: []string{"*"}},
						},
					},
				}},
			},
			wantErr: "",
		},
		{
			name: "valid with wildcard in one role only",
			cfg: Config{
				Projects: []ProjectConfig{{
					Name:      "p1",
					Agent:     AgentConfig{Type: "codex"},
					Platforms: []PlatformConfig{{Type: "telegram", Options: map[string]any{"token": "x"}}},
					Users: &UsersConfig{
						Roles: map[string]RoleConfig{
							"admin":  {UserIDs: []string{"u1"}},
							"member": {UserIDs: []string{"*", "u2"}},
						},
					},
				}},
			},
			wantErr: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateUsersConfig("projects[0]", tt.cfg.Projects[0].Users)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			} else {
				if err == nil {
					t.Error("expected error, got nil")
				} else if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %q, want substring %q", err.Error(), tt.wantErr)
				}
			}
		})
	}
}

// --- cloneStringMap tests ---

func TestCloneStringMap(t *testing.T) {
	// nil map
	if got := cloneStringMap(nil); got != nil {
		t.Errorf("cloneStringMap(nil) = %v, want nil", got)
	}

	// empty map
	empty := cloneStringMap(map[string]string{})
	if got := cloneStringMap(empty); got == nil || len(got) != 0 {
		t.Errorf("cloneStringMap(empty) = %v, want empty non-nil map", got)
	}

	// populated map
	orig := map[string]string{"key1": "val1", "key2": "val2"}
	cloned := cloneStringMap(orig)
	if len(cloned) != len(orig) {
		t.Errorf("length mismatch: got %d, want %d", len(cloned), len(orig))
	}
	for k, v := range orig {
		if cloned[k] != v {
			t.Errorf("cloneStringMap[%q] = %q, want %q", k, cloned[k], v)
		}
	}
	// verify it's a deep copy
	delete(cloned, "key1")
	if _, ok := orig["key1"]; !ok {
		t.Error("cloneStringMap returned same map reference, not a copy")
	}
}

// --- pickAgentTemplateForNewProject tests ---

func TestPickAgentTemplateForNewProject(t *testing.T) {
	baseProj := ProjectConfig{
		Name: "base",
		Agent: AgentConfig{
			Type:    "claudecode",
			Options: map[string]any{"mode": "yolo"},
			Providers: []ProviderConfig{{
				Name:   "openai",
				APIKey: "sk-test",
				Model:  "gpt-4",
			}},
		},
		Platforms: []PlatformConfig{{Type: "telegram", Options: map[string]any{"token": "x"}}},
	}

	t.Run("clone from existing project", func(t *testing.T) {
		cfg := &Config{Projects: []ProjectConfig{baseProj}}
		opts := EnsureProjectWithFeishuOptions{CloneFromProject: "base"}
		got := pickAgentTemplateForNewProject(cfg, opts)
		if got.Type != "claudecode" {
			t.Errorf("Type = %q, want claudecode", got.Type)
		}
		if len(got.Providers) != 1 || got.Providers[0].APIKey != "sk-test" {
			t.Errorf("Providers not cloned correctly")
		}
	})

	t.Run("no clone but has projects", func(t *testing.T) {
		cfg := &Config{Projects: []ProjectConfig{baseProj}}
		opts := EnsureProjectWithFeishuOptions{}
		got := pickAgentTemplateForNewProject(cfg, opts)
		if got.Type != "claudecode" {
			t.Errorf("Type = %q, want claudecode", got.Type)
		}
	})

	t.Run("no projects uses default codex", func(t *testing.T) {
		cfg := &Config{Projects: []ProjectConfig{}}
		opts := EnsureProjectWithFeishuOptions{}
		got := pickAgentTemplateForNewProject(cfg, opts)
		if got.Type != "codex" {
			t.Errorf("Type = %q, want codex", got.Type)
		}
		if got.Options == nil {
			t.Error("Options should not be nil")
		}
	})

	t.Run("no projects with explicit agent type", func(t *testing.T) {
		cfg := &Config{Projects: []ProjectConfig{}}
		opts := EnsureProjectWithFeishuOptions{AgentType: "gemini"}
		got := pickAgentTemplateForNewProject(cfg, opts)
		if got.Type != "gemini" {
			t.Errorf("Type = %q, want gemini", got.Type)
		}
	})

	t.Run("explicit agent type overrides clone from first project", func(t *testing.T) {
		cfg := &Config{Projects: []ProjectConfig{baseProj}}
		opts := EnsureProjectWithFeishuOptions{AgentType: "cursor"}
		got := pickAgentTemplateForNewProject(cfg, opts)
		if got.Type != "cursor" {
			t.Errorf("Type = %q, want cursor (explicit AgentType should take priority over cloning first project)", got.Type)
		}
	})
}

// --- cloneAgentConfig tests ---

func TestCloneAgentConfig(t *testing.T) {
	t.Run("without providers", func(t *testing.T) {
		in := AgentConfig{
			Type:    "codex",
			Options: map[string]any{"mode": "default"},
		}
		got := cloneAgentConfig(in)
		if got.Type != "codex" {
			t.Errorf("Type = %q, want codex", got.Type)
		}
		if got.Options["mode"] != "default" {
			t.Errorf("Options not cloned")
		}
		if len(got.Providers) != 0 {
			t.Errorf("Providers length = %d, want 0", len(got.Providers))
		}
	})

	t.Run("with providers", func(t *testing.T) {
		in := AgentConfig{
			Type:    "claudecode",
			Options: map[string]any{"work_dir": "/tmp/test"},
			Providers: []ProviderConfig{
				{
					Name:     "openai",
					APIKey:   "sk-test",
					BaseURL:  "https://api.openai.com",
					Model:    "gpt-4",
					Thinking: "on",
					Env:      map[string]string{"DEBUG": "1"},
				},
			},
		}
		got := cloneAgentConfig(in)
		if len(got.Providers) != 1 {
			t.Fatalf("Providers length = %d, want 1", len(got.Providers))
		}
		p := got.Providers[0]
		if p.Name != "openai" || p.APIKey != "sk-test" || p.BaseURL != "https://api.openai.com" || p.Model != "gpt-4" {
			t.Errorf("Provider fields not cloned correctly: %+v", p)
		}
		if p.Env["DEBUG"] != "1" {
			t.Errorf("Provider Env not cloned correctly")
		}
		// Verify deep copy of Options
		got.Options["mode"] = "changed"
		if in.Options["mode"] == "changed" {
			t.Error("Options is same reference, not a deep copy")
		}
		// Verify deep copy of Provider Env
		delete(got.Providers[0].Env, "DEBUG")
		if in.Providers[0].Env["DEBUG"] == "" {
			t.Error("Provider Env is same reference, not a deep copy")
		}
	})
}

func TestEnsureProjectWithWeixinPlatform_CreatesMissingProject(t *testing.T) {
	configPath := writeConfigFixture(t, feishuConfigFixture)
	patchConfigPath(t, configPath)

	result, err := EnsureProjectWithWeixinPlatform(EnsureProjectWithWeixinOptions{
		ProjectName: "gamma",
		WorkDir:     "/tmp/gamma",
	})
	if err != nil {
		t.Fatalf("EnsureProjectWithWeixinPlatform returned error: %v", err)
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
	if proj.Platforms[0].Type != "weixin" {
		t.Fatalf("platform type = %q, want weixin", proj.Platforms[0].Type)
	}
}

func TestEnsureProjectWithWeixinPlatform_AddsPlatformWhenMissing(t *testing.T) {
	configPath := writeConfigFixture(t, projectWithoutFeishuFixture)
	patchConfigPath(t, configPath)

	result, err := EnsureProjectWithWeixinPlatform(EnsureProjectWithWeixinOptions{
		ProjectName: "beta",
	})
	if err != nil {
		t.Fatalf("EnsureProjectWithWeixinPlatform returned error: %v", err)
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
	if proj.Platforms[1].Type != "weixin" {
		t.Fatalf("platform type = %q, want weixin", proj.Platforms[1].Type)
	}
}

func TestSaveWeixinPlatformCredentials_UpdateToken(t *testing.T) {
	configPath := writeConfigFixture(t, weixinConfigFixture)
	patchConfigPath(t, configPath)

	_, err := SaveWeixinPlatformCredentials(WeixinCredentialUpdateOptions{
		ProjectName: "alpha",
		Token:       "new_weixin_token",
		BaseURL:     "https://ilinkai.weixin.qq.com",
	})
	if err != nil {
		t.Fatalf("SaveWeixinPlatformCredentials returned error: %v", err)
	}

	cfg := readConfigFixture(t, configPath)
	tok, _ := cfg.Projects[0].Platforms[0].Options["token"].(string)
	if tok != "new_weixin_token" {
		t.Fatalf("token = %q, want new_weixin_token", tok)
	}
	bu, _ := cfg.Projects[0].Platforms[0].Options["base_url"].(string)
	if bu != "https://ilinkai.weixin.qq.com" {
		t.Fatalf("base_url = %q", bu)
	}
}

func TestSaveWeixinPlatformCredentials_AppendsScannedUserToAllowFrom(t *testing.T) {
	configPath := writeConfigFixture(t, strings.Replace(weixinConfigFixture, `base_url = "https://ilink.example"`, "base_url = \"https://ilink.example\"\nallow_from = \"wx_user_1\"", 1))
	patchConfigPath(t, configPath)

	result, err := SaveWeixinPlatformCredentials(WeixinCredentialUpdateOptions{
		ProjectName:       "alpha",
		Token:             "new_weixin_token",
		ScannedUserID:     "wx_user_2",
		SetAllowFromEmpty: true,
	})
	if err != nil {
		t.Fatalf("SaveWeixinPlatformCredentials returned error: %v", err)
	}

	if result.AllowFrom != "wx_user_1,wx_user_2" {
		t.Fatalf("result.AllowFrom = %q, want %q", result.AllowFrom, "wx_user_1,wx_user_2")
	}

	cfg := readConfigFixture(t, configPath)
	if got := stringMapValue(cfg.Projects[0].Platforms[0].Options, "allow_from"); got != "wx_user_1,wx_user_2" {
		t.Fatalf("allow_from = %q, want %q", got, "wx_user_1,wx_user_2")
	}
}

func TestSaveWeixinPlatformCredentials_LeavesWildcardAllowFromUnchanged(t *testing.T) {
	configPath := writeConfigFixture(t, strings.Replace(weixinConfigFixture, `base_url = "https://ilink.example"`, "base_url = \"https://ilink.example\"\nallow_from = \"*\"", 1))
	patchConfigPath(t, configPath)

	result, err := SaveWeixinPlatformCredentials(WeixinCredentialUpdateOptions{
		ProjectName:       "alpha",
		Token:             "new_weixin_token",
		ScannedUserID:     "wx_user_2",
		SetAllowFromEmpty: true,
	})
	if err != nil {
		t.Fatalf("SaveWeixinPlatformCredentials returned error: %v", err)
	}

	if result.AllowFrom != "*" {
		t.Fatalf("result.AllowFrom = %q, want %q", result.AllowFrom, "*")
	}

	cfg := readConfigFixture(t, configPath)
	if got := stringMapValue(cfg.Projects[0].Platforms[0].Options, "allow_from"); got != "*" {
		t.Fatalf("allow_from = %q, want %q", got, "*")
	}
}

func TestSaveProjectSettings_ExtraFields(t *testing.T) {
	configPath := writeConfigFixture(t, feishuConfigFixture)
	patchConfigPath(t, configPath)

	show := true
	wd := "/tmp/patched"
	mode := "yolo"
	err := SaveProjectSettings("alpha", ProjectSettingsUpdate{
		WorkDir:              &wd,
		Mode:                 &mode,
		ShowContextIndicator: &show,
		PlatformAllowFrom:    map[string]string{"telegram": "u1", "Feishu": "u2"},
	})
	if err != nil {
		t.Fatalf("SaveProjectSettings: %v", err)
	}

	cfg := readConfigFixture(t, configPath)
	proj := cfg.Projects[0]
	if stringMapValue(proj.Agent.Options, "work_dir") != wd {
		t.Fatalf("work_dir = %q, want %q", stringMapValue(proj.Agent.Options, "work_dir"), wd)
	}
	if stringMapValue(proj.Agent.Options, "mode") != mode {
		t.Fatalf("mode = %q, want %q", stringMapValue(proj.Agent.Options, "mode"), mode)
	}
	if proj.ShowContextIndicator == nil || !*proj.ShowContextIndicator {
		t.Fatalf("ShowContextIndicator = %v, want true", proj.ShowContextIndicator)
	}
	if stringMapValue(proj.Platforms[0].Options, "allow_from") != "u1" {
		t.Fatalf("telegram allow_from = %q, want u1", stringMapValue(proj.Platforms[0].Options, "allow_from"))
	}
	if stringMapValue(proj.Platforms[1].Options, "allow_from") != "u2" {
		t.Fatalf("feishu allow_from = %q, want u2", stringMapValue(proj.Platforms[1].Options, "allow_from"))
	}
}

func TestGetProjectConfigDetails(t *testing.T) {
	configPath := writeConfigFixture(t, feishuConfigFixture)
	patchConfigPath(t, configPath)

	details := GetProjectConfigDetails("alpha")
	if details == nil {
		t.Fatal("GetProjectConfigDetails returned nil")
	}
	if details["work_dir"] != "/tmp/alpha" {
		t.Fatalf("work_dir = %v", details["work_dir"])
	}
	pcs, ok := details["platform_configs"].([]map[string]any)
	if !ok || len(pcs) < 2 {
		t.Fatalf("platform_configs = %#v", details["platform_configs"])
	}
}

func TestAddPlatformToProject_NewProjectWithAgentTypeAndWorkDir(t *testing.T) {
	configPath := writeConfigFixture(t, feishuConfigFixture)
	patchConfigPath(t, configPath)

	err := AddPlatformToProject("sigma", PlatformConfig{Type: "slack", Options: map[string]any{"token": "x"}}, "/sigma", "gemini")
	if err != nil {
		t.Fatalf("AddPlatformToProject: %v", err)
	}
	cfg := readConfigFixture(t, configPath)
	if len(cfg.Projects) != 2 {
		t.Fatalf("len(projects) = %d, want 2", len(cfg.Projects))
	}
	proj := cfg.Projects[1]
	if proj.Name != "sigma" {
		t.Fatalf("name = %q", proj.Name)
	}
	if proj.Agent.Type != "gemini" {
		t.Fatalf("agent type = %q, want gemini", proj.Agent.Type)
	}
	if stringMapValue(proj.Agent.Options, "work_dir") != "/sigma" {
		t.Fatalf("work_dir = %q", stringMapValue(proj.Agent.Options, "work_dir"))
	}
	if len(proj.Platforms) != 1 || proj.Platforms[0].Type != "slack" {
		t.Fatalf("platforms = %#v", proj.Platforms)
	}
}

func TestAddPlatformToProject_NewProjectClonesAgentWhenAgentTypeEmpty(t *testing.T) {
	configPath := writeConfigFixture(t, feishuConfigFixture)
	patchConfigPath(t, configPath)

	err := AddPlatformToProject("tau", PlatformConfig{Type: "slack", Options: map[string]any{"token": "x"}}, "", "")
	if err != nil {
		t.Fatalf("AddPlatformToProject: %v", err)
	}
	cfg := readConfigFixture(t, configPath)
	proj := cfg.Projects[len(cfg.Projects)-1]
	if proj.Agent.Type != "codex" {
		t.Fatalf("agent type = %q, want codex (cloned)", proj.Agent.Type)
	}
	if stringMapValue(proj.Agent.Options, "work_dir") != "/tmp/alpha" {
		t.Fatalf("cloned work_dir = %q, want /tmp/alpha", stringMapValue(proj.Agent.Options, "work_dir"))
	}
}

func TestFormatTOML(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{
			name:  "collapse multiple blank lines",
			input: "a = 1\n\n\n\nb = 2\n",
			want:  "a = 1\n\nb = 2\n",
		},
		{
			name:  "blank line before section header",
			input: "a = 1\n[section]\nb = 2\n",
			want:  "a = 1\n\n[section]\nb = 2\n",
		},
		{
			name:  "strip trailing whitespace",
			input: "a = 1   \nb = 2\t\n",
			want:  "a = 1\nb = 2\n",
		},
		{
			name:  "remove empty section",
			input: "[empty]\n\n[real]\nk = 1\n",
			want:  "[real]\nk = 1\n",
		},
		{
			name:  "already formatted",
			input: "[section]\na = 1\n",
			want:  "[section]\na = 1\n",
		},
		{
			name:  "preserves comments",
			input: "# comment\na = 1\n\n[section]\n# inline\nb = 2\n",
			want:  "# comment\na = 1\n\n[section]\n# inline\nb = 2\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatTOML(tc.input)
			if got != tc.want {
				t.Errorf("formatTOML:\n  input: %q\n  got:   %q\n  want:  %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestFormatConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	messy := "language = \"en\"   \n\n\n\n[[projects]]\nname = \"test\"\n\n\n[projects.agent]\ntype = \"codex\"\n\n[projects.agent.options]\n\n[[projects.platforms]]\ntype = \"telegram\"\n\n[projects.platforms.options]\ntoken = \"abc\"\n"
	os.WriteFile(path, []byte(messy), 0o644)

	if err := FormatConfigFile(path); err != nil {
		t.Fatalf("FormatConfigFile: %v", err)
	}

	data, _ := os.ReadFile(path)
	content := string(data)

	if strings.Contains(content, "   \n") {
		t.Error("trailing whitespace not stripped")
	}
	if strings.Contains(content, "\n\n\n") {
		t.Error("consecutive blank lines not collapsed")
	}

	cfg := &Config{}
	if _, err := toml.Decode(content, cfg); err != nil {
		t.Fatalf("formatted config is invalid TOML: %v", err)
	}
	if len(cfg.Projects) != 1 || cfg.Projects[0].Name != "test" {
		t.Error("formatting corrupted config content")
	}

	t.Run("no-op when already formatted", func(t *testing.T) {
		before, _ := os.ReadFile(path)
		if err := FormatConfigFile(path); err != nil {
			t.Fatalf("second FormatConfigFile: %v", err)
		}
		after, _ := os.ReadFile(path)
		if string(before) != string(after) {
			t.Error("idempotent format produced different output")
		}
	})

	t.Run("rejects invalid TOML", func(t *testing.T) {
		badPath := filepath.Join(dir, "bad.toml")
		os.WriteFile(badPath, []byte("[invalid\n"), 0o644)
		if err := FormatConfigFile(badPath); err == nil {
			t.Error("expected error for invalid TOML")
		}
	})
}
