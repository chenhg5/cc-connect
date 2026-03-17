package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

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
