package config

import (
	"fmt"
	"sort"
	"strings"
)

// Starter configuration identity. The first-run template writes exactly these
// values, and the startup preflight recognises them again by the same
// constants, so cc-connect-next never has to guess whether a value merely
// looks unfinished.
const (
	StarterProjectName = "my-project"
	StarterAgentType   = "claudecode"

	PlaceholderWorkDir         = "/path/to/your/project"
	PlaceholderFeishuAppID     = "your-feishu-app-id"
	PlaceholderFeishuAppSecret = "your-feishu-app-secret"
)

// starterPlaceholderFixes maps every value the template asks the user to
// replace to the next step that replaces it.
var starterPlaceholderFixes = map[string]string{
	PlaceholderWorkDir:         "set it to the absolute path of the project the agent should work in",
	PlaceholderFeishuAppID:     "run `cc-connect-next feishu setup` to create the app and write both credentials, or paste the App ID from https://open.feishu.cn",
	PlaceholderFeishuAppSecret: "run `cc-connect-next feishu setup` to create the app and write both credentials, or paste the App Secret from https://open.feishu.cn",
}

// StarterConfigTOML renders the configuration written on first run.
//
// The Feishu-shaped tables are generated from RecommendedFeishuProfile rather
// than spelled out again here: the template a new user gets and the profile
// `feishu setup` offers are the same recommendation, so they must not be able
// to disagree. Only credentials, the work directory, and the agent choice are
// left for the user, and each of those is a placeholder the startup preflight
// refuses to run with.
func StarterConfigTOML() string {
	settings := RecommendedFeishuProfile(StarterAgentType)

	var b strings.Builder
	b.WriteString(`# cc-connect-next configuration
# Docs: https://github.com/timmyagentic/cc-connect-next
#
# Values marked REPLACE below are placeholders. cc-connect-next refuses to
# start while any of them is still in place, so a half-edited config fails
# immediately instead of reporting "running" with no platform connected.

[log]
level = "info"

[[projects]]
name = "` + StarterProjectName + `"

[projects.agent]
type = "` + StarterAgentType + `"   # "claudecode", "codex", "cursor", "gemini", "qoder", "opencode", or "iflow"

[projects.agent.options]
work_dir = "` + PlaceholderWorkDir + `"   # REPLACE: absolute path of the project the agent works in
mode = "default"
# model = "claude-sonnet-4-20250514"

# The tables below are the recommended Feishu profile: a quoted answer card
# that carries the final answer and nothing else, clickable file references,
# and a bot that answers its group without being @mentioned every time. They
# are generated from the same definition ` + "`cc-connect-next feishu setup`" + `
# applies, so the two cannot drift apart.
`)

	writeStarterProfileTable(&b, settings, feishuProfileTableDisplay)
	writeStarterProfileTable(&b, settings, feishuProfileTableReferences)

	b.WriteString(`
# --- Choose at least one platform below ---

# Feishu / Lark (WebSocket, no public IP needed)
[[projects.platforms]]
type = "feishu"
`)

	b.WriteString(`
[projects.platforms.options]
app_id = "` + PlaceholderFeishuAppID + `"           # REPLACE: see ` + "`cc-connect-next feishu setup`" + `
app_secret = "` + PlaceholderFeishuAppSecret + `"   # REPLACE: see ` + "`cc-connect-next feishu setup`" + `
`)
	writeStarterProfileSettings(&b, settings, feishuProfileTablePlatform)
	b.WriteString(`# group_reply_all answers every group message without an @mention. Scope who
# may drive the agent before using it in a shared group:
# allow_from = ["ou_your_feishu_open_id"]
# allow_chat = ["oc_your_group_chat_id"]

# For more platforms (DingTalk, Telegram, Slack, Discord, LINE, WeChat Work)
# see: https://github.com/timmyagentic/cc-connect-next/blob/main/config.example.toml
`)

	return b.String()
}

// writeStarterProfileTable writes one profile table, header included.
func writeStarterProfileTable(b *strings.Builder, settings []RecommendedFeishuSetting, table string) {
	header := ""
	for _, s := range settings {
		if s.Table == table {
			header = s.TableHeader()
			break
		}
	}
	if header == "" {
		return
	}
	b.WriteString("\n")
	b.WriteString(header + "\n")
	writeStarterProfileSettings(b, settings, table)
}

// writeStarterProfileSettings writes the key/value lines of one profile table
// into an already-open table.
func writeStarterProfileSettings(b *strings.Builder, settings []RecommendedFeishuSetting, table string) {
	for _, s := range settings {
		if s.Table != table {
			continue
		}
		b.WriteString(s.Key + " = " + s.Value + "\n")
	}
}

// StarterPlaceholder is one configuration value that cc-connect-next generated
// for the user to replace and that is still unreplaced.
type StarterPlaceholder struct {
	Project string
	Table   string
	Key     string
	Value   string
	Fix     string
}

// Location returns the TOML path of the placeholder, e.g.
// "projects.platforms.options.app_id".
func (p StarterPlaceholder) Location() string {
	return p.Table + "." + p.Key
}

// String renders one finding as a single operator-facing line.
func (p StarterPlaceholder) String() string {
	return fmt.Sprintf("[%s] %s = %q", p.Project, p.Location(), p.Value)
}

// FindStarterPlaceholders reports every value still set to something the
// first-run template wrote for the user to replace.
//
// Only exact matches of self-generated placeholders are reported. A value that
// merely looks like an example belongs to the user, and startup never second
// guesses it. Findings come back in configuration order so the same config
// always produces the same message.
func FindStarterPlaceholders(cfg *Config) []StarterPlaceholder {
	if cfg == nil {
		return nil
	}
	var found []StarterPlaceholder
	for _, proj := range cfg.Projects {
		found = append(found, findStarterPlaceholdersIn(proj.Name, "projects.agent.options", proj.Agent.Options)...)
		for _, platform := range proj.Platforms {
			found = append(found, findStarterPlaceholdersIn(proj.Name, "projects.platforms.options", platform.Options)...)
		}
	}
	return found
}

func findStarterPlaceholdersIn(project, table string, options map[string]any) []StarterPlaceholder {
	if len(options) == 0 {
		return nil
	}
	keys := make([]string, 0, len(options))
	for k := range options {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var found []StarterPlaceholder
	for _, key := range keys {
		value, ok := options[key].(string)
		if !ok {
			continue
		}
		fix, isPlaceholder := starterPlaceholderFixes[value]
		if !isPlaceholder {
			continue
		}
		found = append(found, StarterPlaceholder{
			Project: project,
			Table:   table,
			Key:     key,
			Value:   value,
			Fix:     fix,
		})
	}
	return found
}

// ExpandUserPath resolves the "~/" shorthand the same way configuration
// loading does, for callers that need to check a configured path before the
// component that consumes it exists.
func ExpandUserPath(path string) string {
	return expandUserPath(path)
}
