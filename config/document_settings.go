package config

import (
	"reflect"
	"strings"

	"github.com/BurntSushi/toml"
)

func globalSettingsEdits(u GlobalSettingsUpdate) []documentEdit {
	var edits []documentEdit
	add := func(table, key string, value any) {
		edits = append(edits, settingEdit(table, -1, -1, key, value))
	}
	if u.Language != nil {
		add("", "language", *u.Language)
	}
	if u.AttachmentSend != nil {
		add("", "attachment_send", *u.AttachmentSend)
	}
	if u.LogLevel != nil {
		add("log", "level", *u.LogLevel)
	}
	if u.IdleTimeoutMins != nil {
		add("", "idle_timeout_mins", *u.IdleTimeoutMins)
	}
	if u.CommentaryMessages != nil {
		add("display", "commentary_messages", *u.CommentaryMessages)
	}
	if u.ThinkingMessages != nil {
		add("display", "thinking_messages", *u.ThinkingMessages)
	}
	if u.ThinkingMaxLen != nil {
		add("display", "thinking_max_len", *u.ThinkingMaxLen)
	}
	if u.ToolMessages != nil {
		add("display", "tool_messages", *u.ToolMessages)
	}
	if u.ToolMaxLen != nil {
		add("display", "tool_max_len", *u.ToolMaxLen)
	}
	if u.StreamPreviewOn != nil {
		add("stream_preview", "enabled", *u.StreamPreviewOn)
	}
	if u.StreamPreviewIntMs != nil {
		add("stream_preview", "interval_ms", *u.StreamPreviewIntMs)
	}
	if u.RateLimitMax != nil {
		add("rate_limit", "max_messages", *u.RateLimitMax)
	}
	if u.RateLimitWindow != nil {
		add("rate_limit", "window_secs", *u.RateLimitWindow)
	}
	if u.QueueMaxDepth != nil {
		add("queue", "max_depth", *u.QueueMaxDepth)
	}
	return edits
}

func projectSettingsEdits(original []byte, cfg *Config, index int, u ProjectSettingsUpdate) ([]documentEdit, error) {
	var before Config
	if err := toml.Unmarshal(original, &before); err != nil {
		return nil, err
	}
	project := cfg.Projects[index]
	old := before.Projects[index]
	var edits []documentEdit
	add := func(table, key string, value any) {
		edits = append(edits, settingEdit(table, index, -1, key, value))
	}
	remove := func(table, key string) {
		edits = append(edits, deleteSetting(table, index, -1, key))
	}
	if u.Language != nil {
		edits = append(edits, settingEdit("", -1, -1, "language", cfg.Language))
	}
	if u.AdminFrom != nil {
		add("projects", "admin_from", project.AdminFrom)
	}
	if u.DisabledCommands != nil {
		add("projects", "disabled_commands", project.DisabledCommands)
	}
	if u.ShowContextIndicator != nil {
		add("projects", "show_context_indicator", *project.ShowContextIndicator)
	}
	if u.ShowWorkdirIndicator != nil {
		add("projects", "show_workdir_indicator", *project.ShowWorkdirIndicator)
	}
	if u.ReplyFooter != nil {
		add("projects", "reply_footer", *project.ReplyFooter)
	}
	if u.InjectSender != nil {
		add("projects", "inject_sender", *project.InjectSender)
	}
	if u.WorkspaceBaseDir != nil {
		add("projects", "base_dir", project.BaseDir)
	}
	if u.WorkspaceMode != nil {
		if project.Mode == "" {
			remove("projects", "mode")
		} else {
			add("projects", "mode", project.Mode)
		}
	}
	if u.AgentType != nil && project.Agent.Type != old.Agent.Type {
		add("projects.agent", "type", project.Agent.Type)
		if !reflect.DeepEqual(project.Agent.ProviderRefs, old.Agent.ProviderRefs) {
			add("projects.agent", "provider_refs", project.Agent.ProviderRefs)
		}
		if _, existed := old.Agent.Options["provider"]; existed {
			if _, remains := project.Agent.Options["provider"]; !remains {
				remove("projects.agent.options", "provider")
			}
		}
	}
	if u.WorkDir != nil || u.WorkspaceMode != nil {
		if value, ok := project.Agent.Options["work_dir"]; ok {
			add("projects.agent.options", "work_dir", value)
		} else if _, had := old.Agent.Options["work_dir"]; had {
			remove("projects.agent.options", "work_dir")
		}
	}
	if u.Mode != nil {
		if value, ok := project.Agent.Options["mode"]; ok {
			add("projects.agent.options", "mode", value)
		} else if _, had := old.Agent.Options["mode"]; had {
			remove("projects.agent.options", "mode")
		}
	}
	if u.PlatformAllowFrom != nil {
		for i, platform := range project.Platforms {
			for typ := range u.PlatformAllowFrom {
				if strings.EqualFold(strings.TrimSpace(typ), strings.TrimSpace(platform.Type)) {
					value := platform.Options["allow_from"]
					edits = append(edits, settingEdit("projects.platforms.options", index, i, "allow_from", value))
					break
				}
			}
		}
	}
	return edits, nil
}
