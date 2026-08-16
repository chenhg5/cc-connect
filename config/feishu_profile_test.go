package config

import (
	"os"
	"strings"
	"testing"
)

func TestRecommendedFeishuProfileCoversTheDeployedShape(t *testing.T) {
	settings := RecommendedFeishuProfile("codex")

	byKey := make(map[string]RecommendedFeishuSetting, len(settings))
	for _, setting := range settings {
		key := setting.Table + "." + setting.Key
		if _, duplicate := byKey[key]; duplicate {
			t.Fatalf("duplicate profile entry %q", key)
		}
		if setting.Value == "" || setting.Why == "" {
			t.Fatalf("profile entry %q is missing a value or rationale: %+v", key, setting)
		}
		byKey[key] = setting
	}

	want := map[string]string{
		"display.card_mode":              `"rich"`,
		"display.thinking_messages":      "false",
		"display.tool_messages":          "false",
		"display.show_context_indicator": "false",
		"display.reply_footer":           "false",
		"display.hide_agent_footer":      "true",
		"references.normalize_agents":    `["codex"]`,
		"references.render_platforms":    `["feishu"]`,
		"references.display_path":        `"smart"`,
		"references.marker_style":        `"emoji"`,
		"references.enclosure_style":     `"code"`,
		"platform.enable_feishu_card":    "true",
		"platform.reply_to_trigger":      "true",
		"platform.thread_isolation":      "false",
		"platform.done_emoji":            `"Done"`,
		"platform.group_reply_all":       "true",
	}
	for key, value := range want {
		setting, ok := byKey[key]
		if !ok {
			t.Fatalf("profile is missing %q", key)
		}
		if setting.Value != value {
			t.Fatalf("%s = %s, want %s", key, setting.Value, value)
		}
	}
	if len(byKey) != len(want) {
		for key := range byKey {
			if _, expected := want[key]; !expected {
				t.Fatalf("unexpected profile entry %q", key)
			}
		}
	}

	// A profile that does not validate would be worse than no profile at all.
	if err := recommendedFeishuProfileConfig(t, "codex").Validate(); err != nil {
		t.Fatalf("recommended profile does not validate: %v", err)
	}
}

func TestRecommendedFeishuProfileTracksTheAgent(t *testing.T) {
	// normalize_agents only accepts agents whose reference syntax the renderer
	// knows. An unsupported agent must not produce a config that fails to load.
	tests := []struct {
		agent string
		want  string
	}{
		{"codex", `["codex"]`},
		{"claudecode", `["claudecode"]`},
		{"", `["all"]`},
		{"gemini", `["all"]`},
	}
	for _, tt := range tests {
		var got string
		for _, setting := range RecommendedFeishuProfile(tt.agent) {
			if setting.Table == "references" && setting.Key == "normalize_agents" {
				got = setting.Value
			}
		}
		if got != tt.want {
			t.Errorf("normalize_agents for agent %q = %s, want %s", tt.agent, got, tt.want)
		}
		if err := recommendedFeishuProfileConfig(t, tt.agent).Validate(); err != nil {
			t.Errorf("profile for agent %q does not validate: %v", tt.agent, err)
		}
	}
}

func recommendedFeishuProfileConfig(t *testing.T, agent string) *Config {
	t.Helper()
	agentType := agent
	if agentType == "" {
		agentType = "claudecode"
	}
	var display, references, options strings.Builder
	for _, setting := range RecommendedFeishuProfile(agent) {
		line := setting.Key + " = " + setting.Value + "\n"
		switch setting.Table {
		case "display":
			display.WriteString(line)
		case "references":
			references.WriteString(line)
		case "platform":
			options.WriteString(line)
		default:
			t.Fatalf("unknown profile table %q", setting.Table)
		}
	}

	body := `
[[projects]]
name = "profile"

[projects.display]
` + display.String() + `
[projects.references]
` + references.String() + `
[projects.agent]
type = "` + agentType + `"

[[projects.platforms]]
type = "feishu"

[projects.platforms.options]
app_id = "cli_profile"
app_secret = "profile"
` + options.String()

	path := writeConfigForTest(t, body)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v\nconfig:\n%s", err, body)
	}
	return cfg
}

func writeConfigForTest(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := dir + "/config.toml"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestApplyRecommendedFeishuProfileWritesEveryTable(t *testing.T) {
	writeTestConfig(t, `
# keep me
[[projects]]
name = "solo"

[projects.agent]
type = "codex"

[projects.agent.options]
work_dir = "/tmp/x"

[[projects.platforms]]
type = "feishu"

[projects.platforms.options]
app_id = "cli_solo"
app_secret = "solo-secret"
`)

	result, err := ApplyRecommendedFeishuProfile(RecommendedFeishuProfileOptions{ProjectName: "solo"})
	if err != nil {
		t.Fatalf("ApplyRecommendedFeishuProfile() error = %v", err)
	}
	if result.Applied != len(RecommendedFeishuProfile("codex")) {
		t.Fatalf("applied %d settings, want %d", result.Applied, len(RecommendedFeishuProfile("codex")))
	}

	raw, err := os.ReadFile(ConfigPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(raw), "# keep me") {
		t.Fatalf("surgical patch lost comments:\n%s", raw)
	}

	cfg := readTestConfig(t)
	proj := &cfg.Projects[0]
	if proj.Display == nil || proj.Display.CardMode == nil || *proj.Display.CardMode != "rich" {
		t.Fatalf("display = %#v, want card_mode rich", proj.Display)
	}
	if proj.Display.ThinkingMessages == nil || *proj.Display.ThinkingMessages {
		t.Fatalf("thinking_messages = %#v, want explicit false", proj.Display.ThinkingMessages)
	}
	_, thinking, tools, _, _, ctxIndicator, footer, _ := EffectiveDisplay(&cfg, proj)
	if thinking || tools || ctxIndicator || footer {
		t.Fatalf("profile did not pin the final-answer-only surface: %v/%v/%v/%v", thinking, tools, ctxIndicator, footer)
	}
	if strings.Join(proj.References.NormalizeAgents, ",") != "codex" {
		t.Fatalf("normalize_agents = %#v, want the project agent", proj.References.NormalizeAgents)
	}
	if strings.Join(proj.References.RenderPlatforms, ",") != "feishu" {
		t.Fatalf("render_platforms = %#v", proj.References.RenderPlatforms)
	}
	if proj.References.DisplayPath != "smart" || proj.References.MarkerStyle != "emoji" || proj.References.EnclosureStyle != "code" {
		t.Fatalf("references = %#v", proj.References)
	}

	opts := proj.Platforms[0].Options
	if opts["group_reply_all"] != true || opts["reply_to_trigger"] != true || opts["enable_feishu_card"] != true {
		t.Fatalf("platform options = %#v", opts)
	}
	if opts["thread_isolation"] != false || opts["done_emoji"] != "Done" {
		t.Fatalf("platform options = %#v", opts)
	}
	// Credentials must survive untouched.
	if opts["app_id"] != "cli_solo" || opts["app_secret"] != "solo-secret" {
		t.Fatalf("credentials were damaged: %#v", opts)
	}
}

func TestApplyRecommendedFeishuProfileIsIdempotent(t *testing.T) {
	writeTestConfig(t, `
[[projects]]
name = "solo"

[projects.agent]
type = "claudecode"

[[projects.platforms]]
type = "lark"

[projects.platforms.options]
app_id = "cli_solo"
app_secret = "solo-secret"
`)

	if _, err := ApplyRecommendedFeishuProfile(RecommendedFeishuProfileOptions{ProjectName: "solo"}); err != nil {
		t.Fatalf("first apply error = %v", err)
	}
	first, err := os.ReadFile(ConfigPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if _, err := ApplyRecommendedFeishuProfile(RecommendedFeishuProfileOptions{ProjectName: "solo"}); err != nil {
		t.Fatalf("second apply error = %v", err)
	}
	second, err := os.ReadFile(ConfigPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("second apply changed the file:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

func TestApplyRecommendedFeishuProfileRequiresAFeishuPlatform(t *testing.T) {
	writeTestConfig(t, `
[[projects]]
name = "solo"

[projects.agent]
type = "codex"

[[projects.platforms]]
type = "telegram"

[projects.platforms.options]
token = "t"
`)

	_, err := ApplyRecommendedFeishuProfile(RecommendedFeishuProfileOptions{ProjectName: "solo"})
	if err == nil || !strings.Contains(err.Error(), "feishu") {
		t.Fatalf("error = %v, want a missing-feishu-platform refusal", err)
	}
}
