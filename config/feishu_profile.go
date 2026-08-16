package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

// Profile table names. They match the TOML table a setting is written into,
// relative to the project being configured.
const (
	feishuProfileTableDisplay    = "display"
	feishuProfileTableReferences = "references"
	feishuProfileTablePlatform   = "platform"
)

// RecommendedFeishuSetting is one key of the recommended Feishu profile.
//
// Value is already TOML-encoded so the profile has exactly one spelling: the
// one written to the file, the one printed in the setup prompt, and the one
// asserted in tests.
type RecommendedFeishuSetting struct {
	Table string
	Key   string
	Value string
	Why   string
}

// TableHeader returns the TOML header a setting belongs under.
func (s RecommendedFeishuSetting) TableHeader() string {
	switch s.Table {
	case feishuProfileTableDisplay:
		return "[projects.display]"
	case feishuProfileTableReferences:
		return "[projects.references]"
	default:
		return "[projects.platforms.options]"
	}
}

// RecommendedFeishuProfile returns the Feishu deployment this project
// recommends, as the settings that produce it.
//
// It is the shape this project is actually operated with day to day rather than
// a theoretical ideal: a quoted answer card that carries the final answer and
// nothing else, file references rendered so they can be clicked, and a bot that
// participates in its group without being @mentioned every time.
//
// Most of these already match the built-in defaults. The profile still spells
// them out: a recommendation the user accepted should keep producing the same
// chat surface when a future release changes what an unset key means.
//
// agentType selects the reference normalizer. Reference syntax is agent
// specific, so an agent whose syntax the renderer does not know falls back to
// "all" rather than emitting a value that fails validation.
func RecommendedFeishuProfile(agentType string) []RecommendedFeishuSetting {
	normalizeAgent := strings.ToLower(strings.TrimSpace(agentType))
	if _, ok := supportedReferenceAgents[normalizeAgent]; !ok || normalizeAgent == "" || normalizeAgent == "all" {
		normalizeAgent = "all"
	}

	return []RecommendedFeishuSetting{
		{feishuProfileTableDisplay, "card_mode", `"rich"`,
			"Feishu Card 2.0 answer card: one quoted card per turn, counts only, never reasoning text"},
		{feishuProfileTableDisplay, "thinking_messages", "false",
			"keep reasoning out of the chat"},
		{feishuProfileTableDisplay, "tool_messages", "false",
			"keep tool calls and their arguments out of the chat"},
		{feishuProfileTableDisplay, "show_context_indicator", "false",
			"keep model, token and context metadata out of the chat"},
		{feishuProfileTableDisplay, "reply_footer", "false",
			"keep the per-turn status footer out of the chat"},
		{feishuProfileTableDisplay, "hide_agent_footer", "true",
			"strip the equivalent model/token/context lines the agent emits itself"},

		{feishuProfileTableReferences, "normalize_agents", `["` + normalizeAgent + `"]`,
			"rewrite this agent's file references into a consistent form"},
		{feishuProfileTableReferences, "render_platforms", `["feishu"]`,
			"render those references only where Feishu can display them"},
		{feishuProfileTableReferences, "display_path", `"smart"`,
			"shorten long paths while keeping them unambiguous"},
		{feishuProfileTableReferences, "marker_style", `"emoji"`,
			"mark references so they stand out from prose"},
		{feishuProfileTableReferences, "enclosure_style", `"code"`,
			"wrap references in code style so they can be copied cleanly"},

		{feishuProfileTablePlatform, "enable_feishu_card", "true",
			"use the interactive card client instead of plain messages"},
		{feishuProfileTablePlatform, "reply_to_trigger", "true",
			"reply in a quote of the message that triggered the turn"},
		{feishuProfileTablePlatform, "thread_isolation", "false",
			"keep one session per chat instead of one per thread"},
		{feishuProfileTablePlatform, "done_emoji", `"Done"`,
			"react when a turn finishes so the chat pushes a notification"},
		{feishuProfileTablePlatform, "group_reply_all", "true",
			"answer every message in the group without needing an @mention — pair it with allow_from/allow_chat"},
	}
}

// RecommendedFeishuProfileFor returns the profile that would be written for a
// project, resolving the reference normalizer from that project's agent.
//
// It is what the setup command previews before asking, so the preview and the
// write cannot disagree. A config that cannot be read yields the agent-agnostic
// profile rather than an error: the preview is guidance, not a gate.
func RecommendedFeishuProfileFor(projectName string) []RecommendedFeishuSetting {
	agentType := ""
	if ConfigPath != "" {
		if data, err := os.ReadFile(ConfigPath); err == nil {
			cfg := &Config{}
			if err := toml.Unmarshal(data, cfg); err == nil {
				for i := range cfg.Projects {
					if cfg.Projects[i].Name == projectName {
						agentType = cfg.Projects[i].Agent.Type
						break
					}
				}
			}
		}
	}
	return RecommendedFeishuProfile(agentType)
}

// RecommendedFeishuProfileOptions selects what ApplyRecommendedFeishuProfile
// writes into.
type RecommendedFeishuProfileOptions struct {
	ProjectName string
	// PlatformIndex is 1-based among the project's feishu/lark platforms;
	// 0 means the first one, matching the credential writer.
	PlatformIndex int
}

// RecommendedFeishuProfileResult reports what was written.
type RecommendedFeishuProfileResult struct {
	ProjectName  string
	PlatformType string
	Applied      int
}

// ApplyRecommendedFeishuProfile writes the recommended profile into a project's
// display, references, and Feishu platform tables.
//
// It edits the file surgically, so comments, key order, and unrelated settings
// survive, and it is idempotent: applying it twice produces the same bytes.
// Credentials are never touched.
func ApplyRecommendedFeishuProfile(opts RecommendedFeishuProfileOptions) (*RecommendedFeishuProfileResult, error) {
	configMu.Lock()
	defer configMu.Unlock()

	if ConfigPath == "" {
		return nil, fmt.Errorf("config path not set")
	}
	projectName := strings.TrimSpace(opts.ProjectName)
	if projectName == "" {
		return nil, fmt.Errorf("project name is required")
	}
	if opts.PlatformIndex < 0 {
		return nil, fmt.Errorf("platform index must be >= 0")
	}

	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	projectIdx := -1
	for i := range cfg.Projects {
		if cfg.Projects[i].Name == projectName {
			projectIdx = i
			break
		}
	}
	if projectIdx < 0 {
		return nil, fmt.Errorf("project %q not found", projectName)
	}
	proj := &cfg.Projects[projectIdx]

	candidates := make([]int, 0, len(proj.Platforms))
	for i := range proj.Platforms {
		switch strings.ToLower(strings.TrimSpace(proj.Platforms[i].Type)) {
		case "feishu", "lark":
			candidates = append(candidates, i)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("project %q has no feishu/lark platform", projectName)
	}
	targetPos := 0
	if opts.PlatformIndex > 0 {
		targetPos = opts.PlatformIndex - 1
	}
	if targetPos >= len(candidates) {
		return nil, fmt.Errorf("platform index %d out of range: project %q has %d feishu/lark platform(s)",
			opts.PlatformIndex, projectName, len(candidates))
	}
	platformIdx := candidates[targetPos]

	settings := RecommendedFeishuProfile(proj.Agent.Type)
	lines, hadTrailing := splitConfigLines(string(data))
	for _, setting := range settings {
		lines, err = applyRecommendedFeishuSetting(lines, projectIdx, platformIdx, setting)
		if err != nil {
			return nil, err
		}
	}
	if err := writeRawConfig(joinConfigLines(lines, hadTrailing)); err != nil {
		return nil, err
	}

	return &RecommendedFeishuProfileResult{
		ProjectName:  projectName,
		PlatformType: strings.ToLower(strings.TrimSpace(proj.Platforms[platformIdx].Type)),
		Applied:      len(settings),
	}, nil
}

func applyRecommendedFeishuSetting(lines []string, projectIdx, platformIdx int, setting RecommendedFeishuSetting) ([]string, error) {
	spans := buildRawProjectSpans(lines)
	if projectIdx >= len(spans) {
		return nil, fmt.Errorf("project located in parsed config but not raw file")
	}

	switch setting.Table {
	case feishuProfileTableDisplay:
		if spans[projectIdx].displayStart < 0 {
			lines = insertProjectSubTable(lines, spans[projectIdx], "[projects.display]")
			spans = buildRawProjectSpans(lines)
		}
		span := spans[projectIdx]
		return upsertTomlRawKey(lines, span.displayStart+1, span.displayEnd, setting.Key, setting.Value), nil

	case feishuProfileTableReferences:
		if spans[projectIdx].referencesStart < 0 {
			lines = insertProjectSubTable(lines, spans[projectIdx], "[projects.references]")
			spans = buildRawProjectSpans(lines)
		}
		span := spans[projectIdx]
		return upsertTomlRawKey(lines, span.referencesStart+1, span.referencesEnd, setting.Key, setting.Value), nil

	default:
		if platformIdx >= len(spans[projectIdx].platforms) {
			return nil, fmt.Errorf("feishu/lark platform located in parsed config but not raw file")
		}
		if spans[projectIdx].platforms[platformIdx].optionsStart < 0 {
			platform := spans[projectIdx].platforms[platformIdx]
			insertAt := platform.end + 1
			block := make([]string, 0, 3)
			if insertAt > 0 && strings.TrimSpace(lines[insertAt-1]) != "" {
				block = append(block, "")
			}
			block = append(block, "[projects.platforms.options]")
			lines = insertLines(lines, insertAt, block)
			spans = buildRawProjectSpans(lines)
		}
		platform := spans[projectIdx].platforms[platformIdx]
		return upsertTomlRawKey(lines, platform.optionsStart+1, platform.optionsEnd, setting.Key, setting.Value), nil
	}
}

// insertProjectSubTable adds a table header inside a [[projects]] block so it
// belongs to that project and not to a later one.
//
// It lands before the project's first sub-table, but after the profile tables
// themselves, so creating display and then references leaves them in that order
// instead of stacking each new table on top of the previous one.
func insertProjectSubTable(lines []string, span rawProjectSpan, header string) []string {
	insertAt := span.end + 1
	for ln := span.start + 1; ln <= span.end && ln < len(lines); ln++ {
		if isAnyTableHeader(lines[ln]) {
			if matchTableHeader(lines[ln], "[projects.display]") || matchTableHeader(lines[ln], "[projects.references]") {
				continue
			}
			insertAt = ln
			break
		}
		insertAt = ln + 1
	}
	block := []string{header}
	if insertAt > 0 && insertAt <= len(lines) && strings.TrimSpace(lines[insertAt-1]) != "" {
		block = append([]string{""}, block...)
	}
	return insertLines(lines, insertAt, block)
}
