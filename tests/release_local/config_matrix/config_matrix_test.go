package config_matrix

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/config"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func baseProjectTOML(extra string) string {
	return `
data_dir = "` + filepath.ToSlash(os.TempDir()) + `/cc-connect-release-test"
` + extra + `

[[projects]]
name = "release"

[projects.agent]
type = "claudecode"
work_dir = "/tmp/cc-connect-release-work"

[[projects.platforms]]
type = "feishu"
app_id = "cli_release"
app_secret = "secret"
`
}

func TestReleaseConfig_ProjectDisplayOverridesGlobalFromLoadedConfig(t *testing.T) {
	path := writeConfig(t, `
attachment_send = "off"

[display]
mode = "quiet"
card_mode = "rich"
thinking_messages = true
tool_messages = false

[[projects]]
name = "release"
reset_on_idle_mins = 0

[projects.display]
mode = "full"
card_mode = "legacy"
thinking_messages = false
tool_messages = true
thinking_max_len = 111
tool_max_len = 222

[projects.agent]
type = "claudecode"
work_dir = "/tmp/cc-connect-release-work"

[[projects.platforms]]
type = "feishu"
app_id = "cli_release"
app_secret = "secret"
`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.AttachmentSend != "off" {
		t.Fatalf("AttachmentSend = %q, want off", cfg.AttachmentSend)
	}
	if len(cfg.Projects) != 1 {
		t.Fatalf("projects = %d, want 1", len(cfg.Projects))
	}
	proj := &cfg.Projects[0]
	if proj.ResetOnIdleMins == nil || *proj.ResetOnIdleMins != 0 {
		t.Fatalf("ResetOnIdleMins = %#v, want explicit 0", proj.ResetOnIdleMins)
	}

	mode, thinking, tools, thinkingMax, toolMax := config.EffectiveDisplay(cfg, proj)
	if mode != config.DisplayModeFull {
		t.Fatalf("mode = %q, want project full override", mode)
	}
	if thinking {
		t.Fatal("thinking_messages = true, want project override false")
	}
	if !tools {
		t.Fatal("tool_messages = false, want project override true")
	}
	if thinkingMax != 111 || toolMax != 222 {
		t.Fatalf("max lens = %d/%d, want 111/222", thinkingMax, toolMax)
	}
	if got := config.EffectiveCardMode(cfg, proj); got != "legacy" {
		t.Fatalf("card mode = %q, want project legacy override", got)
	}
}

func TestReleaseConfig_DefaultsKeepAttachmentsAndFullDisplayEnabled(t *testing.T) {
	path := writeConfig(t, baseProjectTOML(""))
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.AttachmentSend != "on" {
		t.Fatalf("AttachmentSend = %q, want default on", cfg.AttachmentSend)
	}
	mode, thinking, tools, _, _ := config.EffectiveDisplay(cfg, &cfg.Projects[0])
	if mode != config.DisplayModeFull || !thinking || !tools {
		t.Fatalf("display = mode:%s thinking:%v tools:%v, want full/true/true", mode, thinking, tools)
	}
	if got := config.EffectiveCardMode(cfg, &cfg.Projects[0]); got != "legacy" {
		t.Fatalf("card mode = %q, want default legacy", got)
	}
}

func TestReleaseConfig_InvalidCriticalOptionsFailFast(t *testing.T) {
	tests := []struct {
		name    string
		toml    string
		wantErr string
	}{
		{
			name: "invalid attachment send",
			toml: baseProjectTOML(`
attachment_send = "maybe"
`),
			wantErr: `attachment_send must be "on" or "off"`,
		},
		{
			name: "invalid project display mode",
			toml: `
[[projects]]
name = "release"

[projects.display]
mode = "verbose"

[projects.agent]
type = "claudecode"
work_dir = "/tmp/cc-connect-release-work"

[[projects.platforms]]
type = "feishu"
app_id = "cli_release"
app_secret = "secret"
`,
			wantErr: `projects[0].display.mode must be "full", "compact", or "quiet"`,
		},
		{
			name: "negative reset on idle",
			toml: `
[[projects]]
name = "release"
reset_on_idle_mins = -1

[projects.agent]
type = "claudecode"
work_dir = "/tmp/cc-connect-release-work"

[[projects.platforms]]
type = "feishu"
app_id = "cli_release"
app_secret = "secret"
`,
			wantErr: "reset_on_idle_mins must be >= 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := config.Load(writeConfig(t, tt.toml))
			if err == nil {
				t.Fatal("Load() error = nil, want validation error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Load() error = %q, want contains %q", err.Error(), tt.wantErr)
			}
		})
	}
}
