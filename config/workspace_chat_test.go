package config

import (
	"strings"
	"testing"
)

func workspaceChatValidationConfig() Config {
	enabled := true
	return Config{
		WorkspaceChat: WorkspaceChatConfig{
			Enabled: &enabled, Transports: []string{"web", "wecom"},
			WeCom: WorkspaceChatWeComConfig{BotID: "bot", BotSecret: "secret"},
		},
	}
}

func TestWorkspaceChatConfigValidation(t *testing.T) {
	valid := workspaceChatValidationConfig()
	if err := valid.validatePermissive(); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	tests := []struct {
		name string
		edit func(*Config)
		want string
	}{
		{name: "duplicate platform project", edit: func(c *Config) {
			c.Projects = append(c.Projects,
				ProjectConfig{Name: "duplicate", Agent: AgentConfig{Type: "claudecode"}},
				ProjectConfig{Name: "duplicate", Agent: AgentConfig{Type: "codex"}})
		}, want: "duplicates projects[0].name"},
		{name: "empty transports", edit: func(c *Config) { c.WorkspaceChat.Transports = nil }, want: "requires at least one"},
		{name: "unsupported transport", edit: func(c *Config) { c.WorkspaceChat.Transports = []string{"telegram"} }, want: "unsupported value"},
		{name: "duplicate transport", edit: func(c *Config) { c.WorkspaceChat.Transports = []string{"web", "WEB"} }, want: "duplicate value"},
		{name: "wecom credentials", edit: func(c *Config) { c.WorkspaceChat.WeCom.BotSecret = "" }, want: "bot_id and bot_secret"},
		{name: "duplicate wecom protocol", edit: func(c *Config) {
			c.Projects = []ProjectConfig{{Name: "legacy", Agent: AgentConfig{Type: "codex"}, Platforms: []PlatformConfig{{Type: "wecom"}}}}
		}, want: "duplicates the workspace_chat.wecom transport"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := workspaceChatValidationConfig()
			test.edit(&config)
			err := config.validatePermissive()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validatePermissive() error = %v, want %q", err, test.want)
			}
		})
	}
}
