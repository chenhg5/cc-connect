package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// AgentProfile is the legacy v1 profile format, retained for compatibility.
// New configurations use BotConfig: the Bot owns the backend and role.
// MigrateAgentProfiles exports existing references without inventing projects.
type AgentProfile struct {
	ID     string      `toml:"id"`
	Name   string      `toml:"name,omitempty"`
	Role   string      `toml:"role"`
	Prompt string      `toml:"prompt,omitempty"`
	Agent  AgentConfig `toml:"agent"`
}

func (c *Config) validateAgentProfiles() error {
	seen := make(map[string]bool, len(c.AgentProfiles))
	for _, p := range c.AgentProfiles {
		if !core.ValidBotName(p.ID) {
			return fmt.Errorf("config: invalid agent profile id %q", p.ID)
		}
		if seen[p.ID] {
			return fmt.Errorf("config: duplicate agent profile %q", p.ID)
		}
		seen[p.ID] = true
		if _, err := core.AgentRoleByID(p.Role); err != nil {
			return fmt.Errorf("config: agent profile %q: %w", p.ID, err)
		}
		switch p.Agent.Type {
		case "codex", "claudecode", "acp":
		default:
			return fmt.Errorf("config: agent profile %q requires a supported backend (codex, claudecode, acp)", p.ID)
		}
		for _, key := range []string{"system_prompt", "append_system_prompt"} {
			if value, ok := p.Agent.Options[key]; ok {
				if _, ok := value.(string); !ok {
					return fmt.Errorf("config: agent profile %q: %s must be a string", p.ID, key)
				}
			}
		}
	}
	return nil
}

// EffectiveProject resolves a profile without flattening it into the persisted
// configuration. This prevents generated prompts and inherited options from
// accumulating each time a config mutation reads and writes the file.
func (c *Config) EffectiveProject(proj ProjectConfig) (ProjectConfig, error) {
	if proj.Role != "" {
		return c.effectiveBot(proj)
	}
	if proj.AgentProfile == "" {
		return c.effectiveTeamMember(proj)
	}
	var profile *AgentProfile
	for i := range c.AgentProfiles {
		if c.AgentProfiles[i].ID == proj.AgentProfile {
			profile = &c.AgentProfiles[i]
			break
		}
	}
	if profile == nil {
		return ProjectConfig{}, fmt.Errorf("config: project %q references unknown agent profile %q", proj.Name, proj.AgentProfile)
	}
	if proj.Agent.Type != "" && proj.Agent.Type != profile.Agent.Type {
		return ProjectConfig{}, fmt.Errorf("config: project %q backend conflicts with agent profile %q", proj.Name, profile.ID)
	}
	role, err := core.AgentRoleByID(profile.Role)
	if err != nil {
		return ProjectConfig{}, err
	}
	backend := cloneAgentConfig(profile.Agent)
	backend.ProviderRefs = append([]string(nil), profile.Agent.ProviderRefs...)
	if backend.Options == nil {
		backend.Options = make(map[string]any)
	}
	// Runtime-specific options (for example work_dir) override profile defaults.
	for k, v := range cloneAnyMap(proj.Agent.Options) {
		backend.Options[k] = v
	}
	if proj.Agent.ProviderRefs != nil {
		backend.ProviderRefs = append([]string(nil), proj.Agent.ProviderRefs...)
	}
	if proj.Agent.Providers != nil {
		backend.Providers = cloneAgentConfig(proj.Agent).Providers
	}
	for _, key := range []string{"system_prompt", "append_system_prompt"} {
		if value, ok := backend.Options[key]; ok {
			if _, ok := value.(string); !ok {
				return ProjectConfig{}, fmt.Errorf("config: project %q: %s must be a string", proj.Name, key)
			}
		}
	}
	custom, _ := backend.Options["append_system_prompt"].(string)
	parts := []string{fmt.Sprintf("Agent profile: %s\nRole: %s\nRole template version: %d\n\n%s", profile.ID, role.ID, role.Version, role.Prompt)}
	if profile.Prompt != "" {
		parts = append(parts, "Agent-specific instructions:\n"+profile.Prompt)
	}
	if custom != "" {
		parts = append(parts, custom)
	}
	backend.Options["append_system_prompt"] = strings.Join(parts, "\n\n")
	proj.Agent = backend
	// Resolve global providers after the backend type is known. Raw profile
	// references and prompts remain untouched in c.
	resolved := Config{Providers: c.Providers, Projects: []ProjectConfig{proj}}
	resolved.Projects[0].AgentProfile = ""
	resolved.ResolveProviderRefs()
	proj.Agent = resolved.Projects[0].Agent
	if home, err := os.UserHomeDir(); err == nil {
		if dir, ok := proj.Agent.Options["work_dir"].(string); ok {
			proj.Agent.Options["work_dir"] = expandLeadingHome(dir, home)
		}
	}
	return proj, nil
}
