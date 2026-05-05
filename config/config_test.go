package config

import (
	"os"
	"path/filepath"
	"runtime"
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

func TestRunAsEnv_RejectsDangerousVars(t *testing.T) {
	dangerous := []string{"PATH", "path", "LD_PRELOAD", "HOME", "USER", "SHELL", "SUDO_USER", "SUDO_COMMAND", "LD_LIBRARY_PATH", "DYLD_INSERT_LIBRARIES"}
	for _, v := range dangerous {
		err := validateRunAsEnv("projects[0]", []string{v})
		if err == nil {
			t.Errorf("validateRunAsEnv(%q) = nil, want error", v)
		}
	}

	safe := []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "CUSTOM_VAR"}
	for _, v := range safe {
		err := validateRunAsEnv("projects[0]", []string{v})
		if err != nil {
			t.Errorf("validateRunAsEnv(%q) = %v, want nil", v, err)
		}
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

func TestEffectiveDisplay_ProjectOverride(t *testing.T) {
	tru, fal := true, false
	maxA, maxB := 100, 200

	tests := []struct {
		name           string
		cfg            Config
		proj           ProjectConfig
		wantTM         bool
		wantTool       bool
		wantThinkLen   int
		wantToolMaxLen int
	}{
		{
			name: "project overrides global thinking_messages",
			cfg: Config{
				Display: DisplayConfig{ThinkingMessages: &tru, ToolMessages: &tru},
			},
			proj: ProjectConfig{
				Display: &DisplayConfig{ThinkingMessages: &fal},
			},
			wantTM:         false,
			wantTool:       true,
			wantThinkLen:   300,
			wantToolMaxLen: 500,
		},
		{
			name: "project unset falls back to global",
			cfg: Config{
				Display: DisplayConfig{ThinkingMessages: &fal, ToolMessages: &fal},
			},
			proj: ProjectConfig{
				Display: &DisplayConfig{},
			},
			wantTM:         false,
			wantTool:       false,
			wantThinkLen:   300,
			wantToolMaxLen: 500,
		},
		{
			name: "both unset falls back to default",
			cfg:  Config{},
			proj: ProjectConfig{
				Display: &DisplayConfig{},
			},
			wantTM:         true,
			wantTool:       true,
			wantThinkLen:   300,
			wantToolMaxLen: 500,
		},
		{
			name: "project overrides max-len fields",
			cfg: Config{
				Display: DisplayConfig{ThinkingMaxLen: &maxA, ToolMaxLen: &maxA},
			},
			proj: ProjectConfig{
				Display: &DisplayConfig{ThinkingMaxLen: &maxB, ToolMaxLen: &maxB},
			},
			wantTM:         true,
			wantTool:       true,
			wantThinkLen:   200,
			wantToolMaxLen: 200,
		},
		{
			name: "project quiet still respected when project display unset",
			cfg:  Config{Quiet: &tru},
			proj: ProjectConfig{
				Display: &DisplayConfig{},
			},
			wantTM:         false,
			wantTool:       false,
			wantThinkLen:   300,
			wantToolMaxLen: 500,
		},
		{
			name: "project display.thinking_messages true overrides project quiet",
			cfg:  Config{Quiet: &tru},
			proj: ProjectConfig{
				Display: &DisplayConfig{ThinkingMessages: &tru},
			},
			wantTM:         true,
			wantTool:       false,
			wantThinkLen:   300,
			wantToolMaxLen: 500,
		},
		{
			name: "nil project display behaves like before",
			cfg: Config{
				Display: DisplayConfig{ThinkingMessages: &fal},
			},
			proj:           ProjectConfig{},
			wantTM:         false,
			wantTool:       true,
			wantThinkLen:   300,
			wantToolMaxLen: 500,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tm, tool, thinkLen, toolMaxLen := EffectiveDisplay(&tt.cfg, &tt.proj)
			if tm != tt.wantTM {
				t.Errorf("ThinkingMessages = %v, want %v", tm, tt.wantTM)
			}
			if tool != tt.wantTool {
				t.Errorf("ToolMessages = %v, want %v", tool, tt.wantTool)
			}
			if thinkLen != tt.wantThinkLen {
				t.Errorf("ThinkingMaxLen = %d, want %d", thinkLen, tt.wantThinkLen)
			}
			if toolMaxLen != tt.wantToolMaxLen {
				t.Errorf("ToolMaxLen = %d, want %d", toolMaxLen, tt.wantToolMaxLen)
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

func TestLoad_ResolvesEnvPlaceholders(t *testing.T) {

	root := t.TempDir()
	t.Setenv("CC_ROOT", root)
	t.Setenv("TG_TOKEN", "tg-secret")
	t.Setenv("HOOK_TOKEN", "hook-secret")
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:7890")

	configPath := writeConfigFixture(t, `
 data_dir = "${CC_ROOT}/state"

 [webhook]
 token = "${HOOK_TOKEN}"

 [[projects]]
 name = "demo"

 [projects.agent]
 type = "codex"

 [projects.agent.options]
 work_dir = "${CC_ROOT}/repo"
 note = "prefix-${HOOK_TOKEN}-suffix"
 retries = 3

 [[projects.agent.providers]]
 name = "relay"
 api_key = "${OPENAI_API_KEY}"
 base_url = "https://relay.example/${HOOK_TOKEN}"

 [projects.agent.providers.env]
 HTTP_PROXY = "${HTTP_PROXY}"

 [[projects.platforms]]
 type = "telegram"

 [projects.platforms.options]
 token = "${TG_TOKEN}"
 chat_id = 12345
 `)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if got, want := cfg.DataDir, filepath.Join(root, "state"); got != want {
		t.Fatalf("DataDir = %q, want %q", got, want)
	}
	if got := cfg.Webhook.Token; got != "hook-secret" {
		t.Fatalf("Webhook.Token = %q, want hook-secret", got)
	}
	if got := stringMapValue(cfg.Projects[0].Agent.Options, "work_dir"); got != filepath.Join(root, "repo") {
		t.Fatalf("work_dir = %q, want %q", got, filepath.Join(root, "repo"))
	}
	if got := stringMapValue(cfg.Projects[0].Agent.Options, "note"); got != "prefix-hook-secret-suffix" {
		t.Fatalf("note = %q, want prefix-hook-secret-suffix", got)
	}
	if got := cfg.Projects[0].Agent.Providers[0].APIKey; got != "sk-test" {
		t.Fatalf("provider api_key = %q, want sk-test", got)
	}
	if got := cfg.Projects[0].Agent.Providers[0].Env["HTTP_PROXY"]; got != "http://127.0.0.1:7890" {
		t.Fatalf("provider env HTTP_PROXY = %q, want http://127.0.0.1:7890", got)
	}
	if got := stringMapValue(cfg.Projects[0].Platforms[0].Options, "token"); got != "tg-secret" {
		t.Fatalf("platform token = %q, want tg-secret", got)
	}
	if _, ok := cfg.Projects[0].Platforms[0].Options["chat_id"].(int64); !ok {
		t.Fatalf("chat_id type = %T, want int64", cfg.Projects[0].Platforms[0].Options["chat_id"])
	}
}

func TestLoad_MissingEnvPlaceholderBecomesEmptyString(t *testing.T) {

	configPath := writeConfigFixture(t, `
 [[projects]]
 name = "demo"

 [projects.agent]
 type = "codex"

 [projects.agent.options]
 work_dir = "/tmp/demo"
 retries = 5

 [[projects.agent.providers]]
 name = "relay"
 api_key = "${MISSING_API_KEY}"

 [projects.agent.providers.env]
 HTTPS_PROXY = "${MISSING_PROXY}"

 [[projects.platforms]]
 type = "telegram"

 [projects.platforms.options]
 token = "prefix-${MISSING_TOKEN}-suffix"
 `)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if got := cfg.Projects[0].Agent.Providers[0].APIKey; got != "" {
		t.Fatalf("provider api_key = %q, want empty", got)
	}
	if got := cfg.Projects[0].Agent.Providers[0].Env["HTTPS_PROXY"]; got != "" {
		t.Fatalf("provider env HTTPS_PROXY = %q, want empty", got)
	}
	if got := stringMapValue(cfg.Projects[0].Platforms[0].Options, "token"); got != "prefix--suffix" {
		t.Fatalf("platform token = %q, want prefix--suffix", got)
	}
	if _, ok := cfg.Projects[0].Agent.Options["retries"].(int64); !ok {
		t.Fatalf("retries type = %T, want int64", cfg.Projects[0].Agent.Options["retries"])
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

const providerConfigWithCommentsTOML = `# This is my config file
# Very important - do not lose this!
custom_top = "keep_me"

[[projects]]
name = "demo"
work_dir = "/tmp/demo" # inline comment

[projects.agent]
type = "claudecode"

[projects.agent.options]
mode = "default"
provider = "primary"
custom_option = "still_here" # keep inline comment

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

func TestSaveActiveProvider_PreservesCommentsAndUnknownFields(t *testing.T) {
	writeTestConfig(t, providerConfigWithCommentsTOML)

	if err := SaveActiveProvider("demo", "backup"); err != nil {
		t.Fatalf("SaveActiveProvider() error: %v", err)
	}

	content, err := os.ReadFile(ConfigPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(content)

	if !strings.Contains(text, "# This is my config file") {
		t.Fatalf("expected top comment to be preserved, got:\n%s", text)
	}
	if !strings.Contains(text, "# Very important - do not lose this!") {
		t.Fatalf("expected second comment to be preserved, got:\n%s", text)
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
	if !strings.Contains(text, `mode = "default"`) {
		t.Fatalf("expected mode to be preserved, got:\n%s", text)
	}
	if !strings.Contains(text, `provider = "backup"`) {
		t.Fatalf("expected provider to be updated to backup, got:\n%s", text)
	}
	if !strings.Contains(text, `work_dir = "/tmp/demo"`) {
		t.Fatalf("expected work_dir to be preserved, got:\n%s", text)
	}

	cfg := readTestConfig(t)
	active, _ := cfg.Projects[0].Agent.Options["provider"].(string)
	if active != "backup" {
		t.Fatalf("active provider = %q, want backup", active)
	}
}

func TestSaveAgentModel_PreservesCommentsAndUnknownFields(t *testing.T) {
	writeTestConfig(t, providerConfigWithCommentsTOML)

	if err := SaveAgentModel("demo", "gpt-5.4"); err != nil {
		t.Fatalf("SaveAgentModel() error: %v", err)
	}

	content, err := os.ReadFile(ConfigPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(content)

	if !strings.Contains(text, "# This is my config file") {
		t.Fatalf("expected top comment to be preserved, got:\n%s", text)
	}
	if !strings.Contains(text, `custom_option = "still_here"`) {
		t.Fatalf("expected unknown options field to be preserved, got:\n%s", text)
	}
	if !strings.Contains(text, `provider = "primary"`) {
		t.Fatalf("expected provider to be preserved, got:\n%s", text)
	}
	if !strings.Contains(text, `model = "gpt-5.4"`) {
		t.Fatalf("expected model to be set, got:\n%s", text)
	}
}

func TestSaveProviderModel_PreservesCommentsAndUnknownFields(t *testing.T) {
	writeTestConfig(t, providerConfigWithCommentsTOML)

	if err := SaveProviderModel("demo", "primary", "gpt-5.4"); err != nil {
		t.Fatalf("SaveProviderModel() error: %v", err)
	}

	content, err := os.ReadFile(ConfigPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(content)

	if !strings.Contains(text, "# This is my config file") {
		t.Fatalf("expected top comment to be preserved, got:\n%s", text)
	}
	if !strings.Contains(text, `custom_option = "still_here"`) {
		t.Fatalf("expected unknown options field to be preserved, got:\n%s", text)
	}
	if !strings.Contains(text, `model = "gpt-5.4"`) {
		t.Fatalf("expected model to be set in provider, got:\n%s", text)
	}
}

func TestSaveLanguage_PreservesComments(t *testing.T) {
	writeTestConfig(t, providerConfigWithCommentsTOML)

	if err := SaveLanguage("zh"); err != nil {
		t.Fatalf("SaveLanguage() error: %v", err)
	}

	content, err := os.ReadFile(ConfigPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(content)

	if !strings.Contains(text, "# This is my config file") {
		t.Fatalf("expected top comment to be preserved, got:\n%s", text)
	}
	if !strings.Contains(text, `custom_option = "still_here"`) {
		t.Fatalf("expected unknown options field to be preserved, got:\n%s", text)
	}
	if !strings.Contains(text, `language = "zh"`) {
		t.Fatalf("expected language to be set, got:\n%s", text)
	}

	cfg := readTestConfig(t)
	if cfg.Language != "zh" {
		t.Fatalf("Language = %q, want zh", cfg.Language)
	}
}

func TestSaveDisplayConfig_PreservesComments(t *testing.T) {
	configWithDisplay := providerConfigWithCommentsTOML + `
[display]
# display settings below
thinking_messages = true
custom_display = "keep" # also keep
`
	writeTestConfig(t, configWithDisplay)

	thinking := 200
	toolShow := false
	if err := SaveDisplayConfig(nil, &thinking, nil, &toolShow); err != nil {
		t.Fatalf("SaveDisplayConfig() error: %v", err)
	}

	content, err := os.ReadFile(ConfigPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(content)

	if !strings.Contains(text, "# This is my config file") {
		t.Fatalf("expected top comment to be preserved, got:\n%s", text)
	}
	if !strings.Contains(text, "# display settings below") {
		t.Fatalf("expected display comment to be preserved, got:\n%s", text)
	}
	if !strings.Contains(text, `custom_display = "keep"`) {
		t.Fatalf("expected unknown display field to be preserved, got:\n%s", text)
	}
	if !strings.Contains(text, `thinking_max_len = 200`) {
		t.Fatalf("expected thinking_max_len to be set, got:\n%s", text)
	}
	if !strings.Contains(text, `tool_messages = false`) {
		t.Fatalf("expected tool_messages to be set, got:\n%s", text)
	}
}

func TestSaveTTSMode_PreservesComments(t *testing.T) {
	configWithTTS := providerConfigWithCommentsTOML + `
[tts]
# tts config
tts_mode = "auto"
`
	writeTestConfig(t, configWithTTS)

	if err := SaveTTSMode("always"); err != nil {
		t.Fatalf("SaveTTSMode() error: %v", err)
	}

	content, err := os.ReadFile(ConfigPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(content)

	if !strings.Contains(text, "# This is my config file") {
		t.Fatalf("expected top comment to be preserved, got:\n%s", text)
	}
	if !strings.Contains(text, "# tts config") {
		t.Fatalf("expected tts comment to be preserved, got:\n%s", text)
	}
	if !strings.Contains(text, `tts_mode = "always"`) {
		t.Fatalf("expected tts_mode to be updated, got:\n%s", text)
	}
}

const multiProjectConfigTOML = `# multi-project config
[[projects]]
name = "alpha"
work_dir = "/tmp/alpha"

[projects.agent]
type = "codex"

[projects.agent.options]
provider = "openai"

[[projects.platforms]]
type = "telegram"

[projects.platforms.options]
token = "alpha-token"

[[projects]]
name = "beta"
work_dir = "/tmp/beta"

[projects.agent]
type = "claudecode"

[projects.agent.options]
provider = "anthropic"

[[projects.platforms]]
type = "feishu"

[projects.platforms.options]
app_id = "beta-app"
`

func TestSaveActiveProvider_MultiProject(t *testing.T) {
	writeTestConfig(t, multiProjectConfigTOML)

	if err := SaveActiveProvider("beta", "openai"); err != nil {
		t.Fatalf("SaveActiveProvider() error: %v", err)
	}

	content, err := os.ReadFile(ConfigPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(content)

	if !strings.Contains(text, "# multi-project config") {
		t.Fatalf("expected top comment preserved, got:\n%s", text)
	}

	cfg := readTestConfig(t)
	alphaProvider, _ := cfg.Projects[0].Agent.Options["provider"].(string)
	betaProvider, _ := cfg.Projects[1].Agent.Options["provider"].(string)
	if alphaProvider != "openai" {
		t.Fatalf("alpha provider = %q, want openai (untouched)", alphaProvider)
	}
	if betaProvider != "openai" {
		t.Fatalf("beta provider = %q, want openai (updated)", betaProvider)
	}
}

const globalProviderRefConfigTOML = `# global provider refs
[[providers]]
name = "shared-openai"
api_key = "sk-shared"
model = "gpt-4o"

[[projects]]
name = "demo"
work_dir = "/tmp/demo"

[projects.agent]
type = "codex"
provider_refs = ["shared-openai"]

[[projects.platforms]]
type = "telegram"

[projects.platforms.options]
token = "demo-token"
`

func TestSaveProviderModel_GlobalProviderRef(t *testing.T) {
	writeTestConfig(t, globalProviderRefConfigTOML)

	if err := SaveProviderModel("demo", "shared-openai", "gpt-5"); err != nil {
		t.Fatalf("SaveProviderModel() error: %v", err)
	}

	content, err := os.ReadFile(ConfigPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(content)

	if !strings.Contains(text, "# global provider refs") {
		t.Fatalf("expected comment preserved, got:\n%s", text)
	}
	if !strings.Contains(text, `model = "gpt-5"`) {
		t.Fatalf("expected model updated in global provider, got:\n%s", text)
	}
	if !strings.Contains(text, `api_key = "sk-shared"`) {
		t.Fatalf("expected api_key preserved, got:\n%s", text)
	}

	cfg := readTestConfig(t)
	if cfg.Providers[0].Model != "gpt-5" {
		t.Fatalf("global provider model = %q, want gpt-5", cfg.Providers[0].Model)
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

func TestLoad_ParsesRunAsUser(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("run_as_user is only supported on Linux/macOS")
	}
	configPath := writeConfigFixture(t, projectWithRunAsUserFixture)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got := cfg.Projects[0].RunAsUser; got != "partseeker-coder" {
		t.Fatalf("run_as_user = %q, want %q", got, "partseeker-coder")
	}
	if got := cfg.Projects[0].RunAsEnv; len(got) != 2 || got[0] != "PGSSLROOTCERT" || got[1] != "PGSSLMODE" {
		t.Fatalf("run_as_env = %v, want [PGSSLROOTCERT PGSSLMODE]", got)
	}
}

func TestLoad_RejectsRunAsUserRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("run_as_user is only supported on Linux/macOS")
	}
	configPath := writeConfigFixture(t, projectWithRunAsUserRootFixture)

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("expected error for run_as_user = root")
	}
	if !strings.Contains(err.Error(), "must not be root") {
		t.Fatalf("error = %q, want 'must not be root' validation", err.Error())
	}
}

func TestLoad_RejectsRunAsUserInvalidChars(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("run_as_user is only supported on Linux/macOS")
	}
	configPath := writeConfigFixture(t, projectWithRunAsUserInvalidFixture)

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("expected error for invalid run_as_user")
	}
	if !strings.Contains(err.Error(), "invalid characters") {
		t.Fatalf("error = %q, want 'invalid characters' validation", err.Error())
	}
}

func TestValidateRunAsUser_ValidNames(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("run_as_user is only supported on Linux/macOS")
	}
	valid := []string{"leigh", "partseeker-coder", "user_name", "user.name", "u1", "_internal"}
	for _, name := range valid {
		if err := validateRunAsUser("projects[0]", name); err != nil {
			t.Errorf("validateRunAsUser(%q) = %v, want nil", name, err)
		}
	}
}

func TestValidateRunAsUser_InvalidNames(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("run_as_user is only supported on Linux/macOS")
	}
	invalid := []string{
		"-leading-dash",
		"1leading-digit",
		"has space",
		"has/slash",
		"has;semi",
		"has$dollar",
		"has`tick",
		strings.Repeat("a", 33), // too long
	}
	for _, name := range invalid {
		if err := validateRunAsUser("projects[0]", name); err == nil {
			t.Errorf("validateRunAsUser(%q) = nil, want error", name)
		}
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

func TestLoad_FilterExternalSessionsDefault(t *testing.T) {
	configPath := writeConfigFixture(t, attachmentSendConfigFixture)
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	proj := cfg.Projects[0]
	if proj.FilterExternalSessions != nil {
		t.Fatalf("FilterExternalSessions should be nil by default, got %v", *proj.FilterExternalSessions)
	}
}

func TestLoad_FilterExternalSessionsTrue(t *testing.T) {
	fixture := `
[[projects]]
name = "beta"
filter_external_sessions = true

[projects.agent]
type = "codex"

[projects.agent.options]
work_dir = "/tmp/beta"

[[projects.platforms]]
type = "telegram"

[projects.platforms.options]
token = "test"
`
	configPath := writeConfigFixture(t, fixture)
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	proj := cfg.Projects[0]
	if proj.FilterExternalSessions == nil || !*proj.FilterExternalSessions {
		t.Fatalf("FilterExternalSessions should be true, got %v", proj.FilterExternalSessions)
	}
}

func TestLoad_FilterExternalSessionsFalse(t *testing.T) {
	fixture := `
[[projects]]
name = "gamma"
filter_external_sessions = false

[projects.agent]
type = "codex"

[projects.agent.options]
work_dir = "/tmp/gamma"

[[projects.platforms]]
type = "telegram"

[projects.platforms.options]
token = "test"
`
	configPath := writeConfigFixture(t, fixture)
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	proj := cfg.Projects[0]
	if proj.FilterExternalSessions == nil || *proj.FilterExternalSessions {
		t.Fatalf("FilterExternalSessions should be false, got %v", proj.FilterExternalSessions)
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

const projectWithRunAsUserFixture = `
[[projects]]
name = "sandboxed"
run_as_user = "partseeker-coder"
run_as_env = ["PGSSLROOTCERT", "PGSSLMODE"]

[projects.agent]
type = "claudecode"

[projects.agent.options]
work_dir = "/tmp/sandboxed"

[[projects.platforms]]
type = "slack"

[projects.platforms.options]
app_token = "xapp-token"
bot_token = "xoxb-token"
`

const projectWithRunAsUserRootFixture = `
[[projects]]
name = "bad"
run_as_user = "root"

[projects.agent]
type = "claudecode"

[projects.agent.options]
work_dir = "/tmp/bad"

[[projects.platforms]]
type = "slack"

[projects.platforms.options]
app_token = "xapp-token"
bot_token = "xoxb-token"
`

const projectWithRunAsUserInvalidFixture = `
[[projects]]
name = "bad"
run_as_user = "has space"

[projects.agent]
type = "claudecode"

[projects.agent.options]
work_dir = "/tmp/bad"

[[projects.platforms]]
type = "slack"

[projects.platforms.options]
app_token = "xapp-token"
bot_token = "xoxb-token"
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

func TestResolveProviderRefs(t *testing.T) {
	cfg := &Config{
		Providers: []ProviderConfig{
			{Name: "global-a", APIKey: "key-a", BaseURL: "https://a.com"},
			{Name: "global-b", APIKey: "key-b", BaseURL: "https://b.com"},
		},
		Projects: []ProjectConfig{
			{
				Name: "proj-with-refs",
				Agent: AgentConfig{
					Type:         "claudecode",
					ProviderRefs: []string{"global-a", "global-b"},
				},
			},
			{
				Name: "proj-inline-only",
				Agent: AgentConfig{
					Type: "codex",
					Providers: []ProviderConfig{
						{Name: "inline-p", APIKey: "inline-key"},
					},
				},
			},
			{
				Name: "proj-mixed",
				Agent: AgentConfig{
					Type:         "claudecode",
					ProviderRefs: []string{"global-a", "global-b"},
					Providers: []ProviderConfig{
						{Name: "global-a", APIKey: "override-key", BaseURL: "https://override.com"},
					},
				},
			},
		},
	}

	cfg.ResolveProviderRefs()

	// proj-with-refs: should have both global providers
	p0 := cfg.Projects[0].Agent.Providers
	if len(p0) != 2 {
		t.Fatalf("proj-with-refs: expected 2 providers, got %d", len(p0))
	}
	if p0[0].Name != "global-a" || p0[0].APIKey != "key-a" {
		t.Errorf("proj-with-refs[0]: expected global-a/key-a, got %s/%s", p0[0].Name, p0[0].APIKey)
	}
	if p0[1].Name != "global-b" || p0[1].APIKey != "key-b" {
		t.Errorf("proj-with-refs[1]: expected global-b/key-b, got %s/%s", p0[1].Name, p0[1].APIKey)
	}

	// proj-inline-only: should remain unchanged
	p1 := cfg.Projects[1].Agent.Providers
	if len(p1) != 1 || p1[0].Name != "inline-p" {
		t.Errorf("proj-inline-only: expected 1 inline provider, got %d", len(p1))
	}

	// proj-mixed: inline override takes precedence for global-a, global-b from ref
	p2 := cfg.Projects[2].Agent.Providers
	if len(p2) != 2 {
		t.Fatalf("proj-mixed: expected 2 providers, got %d", len(p2))
	}
	// global-b is resolved from ref (since no inline override)
	if p2[0].Name != "global-b" || p2[0].APIKey != "key-b" {
		t.Errorf("proj-mixed[0]: expected global-b from ref, got %s/%s", p2[0].Name, p2[0].APIKey)
	}
	// global-a is from inline override
	if p2[1].Name != "global-a" || p2[1].APIKey != "override-key" {
		t.Errorf("proj-mixed[1]: expected global-a override, got %s/%s", p2[1].Name, p2[1].APIKey)
	}
}

func TestResolveProviderRefs_MissingRef(t *testing.T) {
	cfg := &Config{
		Providers: []ProviderConfig{
			{Name: "exists", APIKey: "key"},
		},
		Projects: []ProjectConfig{
			{
				Name: "proj",
				Agent: AgentConfig{
					Type:         "claudecode",
					ProviderRefs: []string{"exists", "nonexistent"},
				},
			},
		},
	}

	cfg.ResolveProviderRefs()

	providers := cfg.Projects[0].Agent.Providers
	if len(providers) != 1 || providers[0].Name != "exists" {
		t.Errorf("expected 1 resolved provider 'exists', got %d: %+v", len(providers), providers)
	}
}

func TestResolveProviderRefs_AgentTypeFiltering(t *testing.T) {
	cfg := &Config{
		Providers: []ProviderConfig{
			{Name: "claude-only", APIKey: "key-c", AgentTypes: []string{"claudecode"}},
			{Name: "codex-only", APIKey: "key-x", AgentTypes: []string{"codex"}},
			{Name: "universal", APIKey: "key-u"}, // no agent_types = works for all
		},
		Projects: []ProjectConfig{
			{
				Name: "proj-claude",
				Agent: AgentConfig{
					Type:         "claudecode",
					ProviderRefs: []string{"claude-only", "codex-only", "universal"},
				},
			},
			{
				Name: "proj-codex",
				Agent: AgentConfig{
					Type:         "codex",
					ProviderRefs: []string{"claude-only", "codex-only", "universal"},
				},
			},
		},
	}

	cfg.ResolveProviderRefs()

	// claudecode project: gets claude-only + universal, skips codex-only
	p0 := cfg.Projects[0].Agent.Providers
	if len(p0) != 2 {
		t.Fatalf("proj-claude: expected 2 providers, got %d: %+v", len(p0), p0)
	}
	if p0[0].Name != "claude-only" {
		t.Errorf("proj-claude[0]: expected claude-only, got %s", p0[0].Name)
	}
	if p0[1].Name != "universal" {
		t.Errorf("proj-claude[1]: expected universal, got %s", p0[1].Name)
	}

	// codex project: gets codex-only + universal, skips claude-only
	p1 := cfg.Projects[1].Agent.Providers
	if len(p1) != 2 {
		t.Fatalf("proj-codex: expected 2 providers, got %d: %+v", len(p1), p1)
	}
	if p1[0].Name != "codex-only" {
		t.Errorf("proj-codex[0]: expected codex-only, got %s", p1[0].Name)
	}
	if p1[1].Name != "universal" {
		t.Errorf("proj-codex[1]: expected universal, got %s", p1[1].Name)
	}
}

func TestResolveProviderRefs_NoGlobalProviders(t *testing.T) {
	cfg := &Config{
		Projects: []ProjectConfig{
			{
				Name: "proj",
				Agent: AgentConfig{
					Type:         "claudecode",
					ProviderRefs: []string{"foo"},
					Providers: []ProviderConfig{
						{Name: "bar", APIKey: "key"},
					},
				},
			},
		},
	}

	cfg.ResolveProviderRefs()

	providers := cfg.Projects[0].Agent.Providers
	if len(providers) != 1 || providers[0].Name != "bar" {
		t.Errorf("expected only inline provider 'bar', got %+v", providers)
	}
}

func TestResolveProviderRefs_Basic(t *testing.T) {
	cfg := &Config{
		Providers: []ProviderConfig{
			{Name: "global1", APIKey: "key1", BaseURL: "https://example.com", Model: "model-a"},
		},
		Projects: []ProjectConfig{{
			Name: "proj",
			Agent: AgentConfig{
				Type:         "claudecode",
				ProviderRefs: []string{"global1"},
			},
		}},
	}
	cfg.ResolveProviderRefs()

	ps := cfg.Projects[0].Agent.Providers
	if len(ps) != 1 || ps[0].Name != "global1" || ps[0].BaseURL != "https://example.com" {
		t.Fatalf("expected resolved global1, got %+v", ps)
	}
}

func TestResolveProviderRefs_AgentTypesFilter(t *testing.T) {
	cfg := &Config{
		Providers: []ProviderConfig{
			{Name: "claude-only", AgentTypes: []string{"claudecode"}},
			{Name: "codex-only", AgentTypes: []string{"codex"}},
			{Name: "universal"},
		},
		Projects: []ProjectConfig{{
			Name: "codex-proj",
			Agent: AgentConfig{
				Type:         "codex",
				ProviderRefs: []string{"claude-only", "codex-only", "universal"},
			},
		}},
	}
	cfg.ResolveProviderRefs()

	ps := cfg.Projects[0].Agent.Providers
	names := make([]string, len(ps))
	for i, p := range ps {
		names[i] = p.Name
	}
	if len(ps) != 2 {
		t.Fatalf("expected 2 providers (codex-only + universal), got %v", names)
	}
	if names[0] != "codex-only" || names[1] != "universal" {
		t.Fatalf("unexpected providers: %v", names)
	}
}

func TestResolveProviderRefs_EndpointsOverride(t *testing.T) {
	cfg := &Config{
		Providers: []ProviderConfig{{
			Name:    "multi",
			BaseURL: "https://provider.com/api",
			Model:   "claude-sonnet-4",
			Endpoints: map[string]string{
				"codex": "https://provider.com/api/v1",
			},
			AgentModels: map[string]string{
				"codex": "openai/gpt-5.3-codex",
			},
		}},
		Projects: []ProjectConfig{
			{
				Name: "claude-proj",
				Agent: AgentConfig{
					Type:         "claudecode",
					ProviderRefs: []string{"multi"},
				},
			},
			{
				Name: "codex-proj",
				Agent: AgentConfig{
					Type:         "codex",
					ProviderRefs: []string{"multi"},
				},
			},
		},
	}
	cfg.ResolveProviderRefs()

	// claudecode project: should keep original base_url and model
	cp := cfg.Projects[0].Agent.Providers
	if len(cp) != 1 {
		t.Fatalf("claude-proj: expected 1 provider, got %d", len(cp))
	}
	if cp[0].BaseURL != "https://provider.com/api" {
		t.Errorf("claude-proj: base_url = %q, want original", cp[0].BaseURL)
	}
	if cp[0].Model != "claude-sonnet-4" {
		t.Errorf("claude-proj: model = %q, want original", cp[0].Model)
	}

	// codex project: should have overridden base_url and model
	xp := cfg.Projects[1].Agent.Providers
	if len(xp) != 1 {
		t.Fatalf("codex-proj: expected 1 provider, got %d", len(xp))
	}
	if xp[0].BaseURL != "https://provider.com/api/v1" {
		t.Errorf("codex-proj: base_url = %q, want codex endpoint", xp[0].BaseURL)
	}
	if xp[0].Model != "openai/gpt-5.3-codex" {
		t.Errorf("codex-proj: model = %q, want codex model", xp[0].Model)
	}
}

func TestResolveProviderRefs_SplitProviderPattern(t *testing.T) {
	cfg := &Config{
		Providers: []ProviderConfig{
			{
				Name:       "ssy",
				APIKey:     "key-xxx",
				BaseURL:    "https://router.example.com/api",
				Model:      "claude-sonnet-4-6",
				AgentTypes: []string{"claudecode", "gemini"},
				Models: []ProviderModelConfig{
					{Model: "claude-sonnet-4-6"},
					{Model: "claude-opus-4"},
				},
			},
			{
				Name:       "ssy-codex",
				APIKey:     "key-xxx",
				BaseURL:    "https://router.example.com/api/v1",
				Model:      "openai/gpt-5.3-codex",
				AgentTypes: []string{"codex"},
				Models: []ProviderModelConfig{
					{Model: "openai/gpt-5.3-codex"},
					{Model: "openai/gpt-5.4"},
				},
				Codex: &CodexProviderConfig{WireAPI: "responses"},
			},
		},
		Projects: []ProjectConfig{
			{
				Name: "my-claude",
				Agent: AgentConfig{
					Type:         "claudecode",
					ProviderRefs: []string{"ssy", "ssy-codex"},
				},
			},
			{
				Name: "my-codex",
				Agent: AgentConfig{
					Type:         "codex",
					ProviderRefs: []string{"ssy", "ssy-codex"},
				},
			},
		},
	}
	cfg.ResolveProviderRefs()

	// claudecode project should only get "ssy" (not ssy-codex)
	cp := cfg.Projects[0].Agent.Providers
	if len(cp) != 1 || cp[0].Name != "ssy" {
		names := make([]string, len(cp))
		for i, p := range cp {
			names[i] = p.Name
		}
		t.Fatalf("claude project: expected [ssy], got %v", names)
	}
	if len(cp[0].Models) != 2 || cp[0].Models[0].Model != "claude-sonnet-4-6" {
		t.Errorf("claude project: unexpected models: %+v", cp[0].Models)
	}

	// codex project should only get "ssy-codex" (not ssy)
	xp := cfg.Projects[1].Agent.Providers
	if len(xp) != 1 || xp[0].Name != "ssy-codex" {
		names := make([]string, len(xp))
		for i, p := range xp {
			names[i] = p.Name
		}
		t.Fatalf("codex project: expected [ssy-codex], got %v", names)
	}
	if xp[0].BaseURL != "https://router.example.com/api/v1" {
		t.Errorf("codex project: base_url = %q", xp[0].BaseURL)
	}
	if xp[0].Model != "openai/gpt-5.3-codex" {
		t.Errorf("codex project: model = %q", xp[0].Model)
	}
	if xp[0].Codex == nil || xp[0].Codex.WireAPI != "responses" {
		t.Errorf("codex project: codex config missing or wrong: %+v", xp[0].Codex)
	}
}

func TestResolveProviderRefs_InlineOverridesGlobal(t *testing.T) {
	cfg := &Config{
		Providers: []ProviderConfig{
			{Name: "global1", BaseURL: "https://global.com", Model: "global-model"},
		},
		Projects: []ProjectConfig{{
			Name: "proj",
			Agent: AgentConfig{
				Type:         "claudecode",
				ProviderRefs: []string{"global1"},
				Providers: []ProviderConfig{
					{Name: "global1", BaseURL: "https://override.com", Model: "override-model"},
				},
			},
		}},
	}
	cfg.ResolveProviderRefs()

	ps := cfg.Projects[0].Agent.Providers
	if len(ps) != 1 {
		t.Fatalf("expected 1 provider (inline override), got %d", len(ps))
	}
	if ps[0].BaseURL != "https://override.com" {
		t.Errorf("inline override not applied: base_url = %q", ps[0].BaseURL)
	}
}

func TestResolveProviderRefs_TOMLParsing(t *testing.T) {
	input := `
[[providers]]
  name = "ssy"
  api_key = "key123"
  base_url = "https://router.example.com/api"
  model = "claude-sonnet-4-6"
  agent_types = ["claudecode", "gemini"]

  [[providers.models]]
    model = "claude-sonnet-4-6"

[[providers]]
  name = "ssy-codex"
  api_key = "key123"
  base_url = "https://router.example.com/api/v1"
  model = "openai/gpt-5.3-codex"
  agent_types = ["codex"]

  [providers.endpoints]
    codex = "https://router.example.com/api/v1"

  [providers.agent_models]
    codex = "openai/gpt-5.3-codex"

  [[providers.models]]
    model = "openai/gpt-5.3-codex"

  [providers.codex]
    wire_api = "responses"

[[projects]]
  name = "test-codex"

  [projects.agent]
    type = "codex"
    provider_refs = ["ssy", "ssy-codex"]

  [[projects.platforms]]
    type = "feishu"
    [projects.platforms.options]
      app_id = "test"
      app_secret = "test"
`
	var cfg Config
	if _, err := toml.Decode(input, &cfg); err != nil {
		t.Fatalf("TOML decode: %v", err)
	}

	if len(cfg.Providers) != 2 {
		t.Fatalf("expected 2 global providers, got %d", len(cfg.Providers))
	}

	codexProv := cfg.Providers[1]
	if codexProv.Codex == nil {
		t.Fatal("ssy-codex: codex config not parsed")
	}
	if codexProv.Codex.WireAPI != "responses" {
		t.Errorf("ssy-codex: wire_api = %q, want responses", codexProv.Codex.WireAPI)
	}
	if codexProv.Endpoints["codex"] != "https://router.example.com/api/v1" {
		t.Errorf("ssy-codex: endpoints not parsed: %+v", codexProv.Endpoints)
	}

	cfg.ResolveProviderRefs()

	ps := cfg.Projects[0].Agent.Providers
	if len(ps) != 1 || ps[0].Name != "ssy-codex" {
		names := make([]string, len(ps))
		for i, p := range ps {
			names[i] = p.Name
		}
		t.Fatalf("expected [ssy-codex], got %v", names)
	}
}

func TestRemoveGlobalProvider_CleansUpProviderRefs(t *testing.T) {
	input := `
[[providers]]
  name = "prov-a"
  api_key = "key-a"

[[providers]]
  name = "prov-b"
  api_key = "key-b"

[[projects]]
  name = "proj1"
  [projects.agent]
    type = "claudecode"
    provider_refs = ["prov-a", "prov-b"]
  [[projects.platforms]]
    type = "feishu"
    [projects.platforms.options]
      app_id = "x"
      app_secret = "y"

[[projects]]
  name = "proj2"
  [projects.agent]
    type = "codex"
    provider_refs = ["prov-a"]
  [[projects.platforms]]
    type = "telegram"
    [projects.platforms.options]
      token = "t"
`
	writeTestConfig(t, input)

	if err := RemoveGlobalProvider("prov-a"); err != nil {
		t.Fatalf("RemoveGlobalProvider: %v", err)
	}

	cfg, err := loadLocked()
	if err != nil {
		t.Fatalf("loadLocked: %v", err)
	}

	if len(cfg.Providers) != 1 || cfg.Providers[0].Name != "prov-b" {
		t.Fatalf("expected only prov-b remaining, got %v", cfg.Providers)
	}

	refs1 := cfg.Projects[0].Agent.ProviderRefs
	if len(refs1) != 1 || refs1[0] != "prov-b" {
		t.Errorf("proj1 provider_refs: want [prov-b], got %v", refs1)
	}

	refs2 := cfg.Projects[1].Agent.ProviderRefs
	if len(refs2) != 0 {
		t.Errorf("proj2 provider_refs: want [], got %v", refs2)
	}
}
