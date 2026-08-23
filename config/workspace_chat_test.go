package config

import (
	"strings"
	"testing"
)

func workspaceChatValidationConfig() Config {
	enabled := true
	return Config{
		Management:    ManagementConfig{Enabled: &enabled},
		WorkspaceChat: WorkspaceChatConfig{Enabled: &enabled, TemplateProject: "codex-template", Transports: []string{"web", "wecom"}},
		Projects: []ProjectConfig{{
			Name:  "codex-template",
			Agent: AgentConfig{Type: "codex", Options: map[string]any{"backend": "app_server"}},
		}},
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
		{name: "missing template", edit: func(c *Config) { c.WorkspaceChat.TemplateProject = "" }, want: "template_project is required"},
		{name: "unknown template", edit: func(c *Config) { c.WorkspaceChat.TemplateProject = "missing" }, want: "does not exist"},
		{name: "non codex agent", edit: func(c *Config) { c.Projects[0].Agent.Type = "claudecode" }, want: "must use the codex agent"},
		{name: "non app server backend", edit: func(c *Config) { c.Projects[0].Agent.Options["backend"] = "exec" }, want: "backend = \"app_server\""},
		{name: "legacy appserver alias", edit: func(c *Config) { c.Projects[0].Agent.Options["backend"] = "appserver" }, want: "backend = \"app_server\""},
		{name: "legacy app-server alias", edit: func(c *Config) { c.Projects[0].Agent.Options["backend"] = "app-server" }, want: "backend = \"app_server\""},
		{name: "uppercase backend alias", edit: func(c *Config) { c.Projects[0].Agent.Options["backend"] = "APP_SERVER" }, want: "backend = \"app_server\""},
		{name: "duplicate template project", edit: func(c *Config) {
			c.Projects = append(c.Projects, ProjectConfig{Name: "codex-template", Agent: AgentConfig{Type: "claudecode"}})
		}, want: "duplicates projects[0].name"},
		{name: "empty transports", edit: func(c *Config) { c.WorkspaceChat.Transports = nil }, want: "requires at least one"},
		{name: "unsupported transport", edit: func(c *Config) { c.WorkspaceChat.Transports = []string{"telegram"} }, want: "unsupported value"},
		{name: "duplicate transport", edit: func(c *Config) { c.WorkspaceChat.Transports = []string{"web", "WEB"} }, want: "duplicate value"},
		{name: "web management disabled", edit: func(c *Config) { disabled := false; c.Management.Enabled = &disabled }, want: "requires management.enabled"},
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
