package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// BotConfig owns the execution backend and messaging identity. ProjectConfig is
// retained as the runtime options vocabulary, not as a business project.
type BotConfig ProjectConfig

type BusinessProjectConfig = core.BusinessProject

// RuntimeProjects projects configuration into the existing engine constructor.
// The returned configurations never replace the serialized source model.
func (c *Config) RuntimeProjects() []ProjectConfig {
	out := append([]ProjectConfig(nil), c.Projects...)
	for _, bot := range c.Bots {
		p := ProjectConfig(bot)
		p.Name = bot.ID // stable runtime identity, independent of the display name
		out = append(out, p)
	}
	return out
}

func (c *Config) validateBots() error {
	ids := make(map[string]bool)
	apps := make(map[string]bool)
	if len(c.Bots) == 0 && len(c.BusinessProjects) == 0 {
		return nil
	}
	for _, p := range c.Projects {
		ids[p.Name] = true
	}
	for _, p := range c.RuntimeProjects() {
		for _, platform := range p.Platforms {
			if app, ok := platform.Options["app_id"].(string); ok && app != "" {
				if apps[app] {
					return fmt.Errorf("config: duplicate application identity")
				}
				apps[app] = true
			}
		}
	}
	for _, b := range c.Bots {
		if !core.ValidBotName(b.ID) || ids[b.ID] {
			return fmt.Errorf("config: invalid or duplicate bot id %q", b.ID)
		}
		ids[b.ID] = true
		if b.AgentProfile != "" {
			return fmt.Errorf("config: bot %q owns its backend; agent_profile is legacy-only", b.ID)
		}
		if _, err := core.AgentRoleByID(b.Role); err != nil {
			return fmt.Errorf("config: bot %q: %w", b.ID, err)
		}
		if b.Agent.Type == "" {
			return fmt.Errorf("config: bot %q requires agent.type", b.ID)
		}
		if b.Mode != "" || b.BaseDir != "" {
			return fmt.Errorf("config: bot %q uses business_projects instead of multi-workspace", b.ID)
		}
	}
	projectIDs := make(map[string]bool)
	for _, p := range c.BusinessProjects {
		if err := p.Validate(); err != nil {
			return fmt.Errorf("config: %w", err)
		}
		if projectIDs[p.ID] {
			return fmt.Errorf("config: duplicate business project %q", p.ID)
		}
		projectIDs[p.ID] = true
		for _, id := range p.BotIDs {
			found := false
			for _, b := range c.Bots {
				if b.ID == id {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("config: project %q references unknown bot %q", p.ID, id)
			}
		}
	}
	return nil
}

func (c *Config) effectiveBot(p ProjectConfig) (ProjectConfig, error) {
	if p.AgentProfile != "" {
		return ProjectConfig{}, fmt.Errorf("config: role and legacy agent_profile cannot be combined")
	}
	role, err := core.AgentRoleByID(p.Role)
	if err != nil {
		return ProjectConfig{}, err
	}
	p.Agent = cloneAgentConfig(p.Agent)
	if p.Agent.Options == nil {
		p.Agent.Options = make(map[string]any)
	}
	for _, key := range []string{"system_prompt", "append_system_prompt"} {
		if val, ok := p.Agent.Options[key]; ok {
			if _, ok := val.(string); !ok {
				return ProjectConfig{}, fmt.Errorf("config: %s must be a string", key)
			}
		}
	}
	extra, _ := p.Agent.Options["append_system_prompt"].(string)
	parts := []string{fmt.Sprintf("Bot: %s\nRole: %s\nRole template version: %d\n\n%s", p.ID, role.ID, role.Version, role.Prompt)}
	if p.Prompt != "" {
		parts = append(parts, "Bot instructions:\n"+p.Prompt)
	}
	if extra != "" {
		parts = append(parts, extra)
	}
	p.Agent.Options["append_system_prompt"] = strings.Join(parts, "\n\n")
	resolved := Config{Providers: c.Providers, Projects: []ProjectConfig{p}}
	resolved.Projects[0].Role = ""
	resolved.ResolveProviderRefs()
	p.Agent = resolved.Projects[0].Agent
	if home, err := os.UserHomeDir(); err == nil {
		if dir, ok := p.Agent.Options["work_dir"].(string); ok {
			p.Agent.Options["work_dir"] = expandLeadingHome(dir, home)
		}
	}
	return p, nil
}

// MigrateAgentProfiles returns a detached v2 config without touching disk. Bot
// IDs retain legacy runtime names, and no business project or history is inferred.
func (c *Config) MigrateAgentProfiles() (*Config, error) {
	out := *c
	out.Bots = append([]BotConfig(nil), c.Bots...)
	out.Projects = nil
	usedProfiles := map[string]bool{}
	for _, p := range c.Projects {
		if p.AgentProfile == "" {
			out.Projects = append(out.Projects, p)
			continue
		}
		var profile *AgentProfile
		for i := range c.AgentProfiles {
			if c.AgentProfiles[i].ID == p.AgentProfile {
				profile = &c.AgentProfiles[i]
				break
			}
		}
		if profile == nil {
			return nil, fmt.Errorf("unknown agent profile %q", p.AgentProfile)
		}
		// Resolve the backend/provider overrides using the existing compatibility
		// path, but do not persist its generated role prompt.
		effective, err := c.EffectiveProject(p)
		if err != nil {
			return nil, err
		}
		extra := profile.Agent.Options["append_system_prompt"]
		if val, ok := p.Agent.Options["append_system_prompt"]; ok {
			extra = val
		}
		delete(effective.Agent.Options, "append_system_prompt")
		if extra != nil {
			effective.Agent.Options["append_system_prompt"] = extra
		}
		usedProfiles[p.AgentProfile] = true
		effective.ID = p.Name
		effective.Role, effective.Prompt = profile.Role, profile.Prompt
		effective.AgentProfile = ""
		out.Bots = append(out.Bots, BotConfig(effective))
	}
	out.AgentProfiles = nil
	for _, profile := range c.AgentProfiles {
		if !usedProfiles[profile.ID] {
			out.AgentProfiles = append(out.AgentProfiles, profile)
		}
	}
	if err := out.validateBots(); err != nil {
		return nil, err
	}
	return &out, nil
}

// BotProjectRuntime snapshots the requested project without mutating the bot.
func BotProjectRuntime(bot ProjectConfig, project core.BusinessProject) ProjectConfig {
	bot.Agent = cloneAgentConfig(bot.Agent)
	if bot.Agent.Options == nil {
		bot.Agent.Options = map[string]any{}
	}
	bot.Agent.Options["work_dir"] = project.WorkDir()
	bot.ContextProjectID = project.ID
	// The transport owner performs identity checks before forwarding.
	bot.BotOwnerClaimHash, bot.BotOwnerID = "", ""
	bot.Prompt += fmt.Sprintf("\nProject: %s\nGoal: %s\nProject rules:\n%s", project.ID, project.Goal, project.Rules)
	return bot
}
