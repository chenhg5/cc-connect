package config

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
	"github.com/chenhg5/cc-connect/core"
)

// ConfigureTeamBot atomically joins an ordinary runtime to its manager's team.
// Backend changes discard inherited backend-specific credentials and options.
func ConfigureTeamBot(parent, name, role, backend, prompt string) error {
	return configureTeamBot(parent, name, role, backend, prompt, nil)
}

// ConfigureTeamBotProfile also stores a verified platform display name.
func ConfigureTeamBotProfile(parent, name, role, backend, prompt, displayName string) error {
	return configureTeamBot(parent, name, role, backend, prompt, &displayName)
}

func configureTeamBot(parent, name, role, backend, prompt string, displayName *string) error {
	configMu.Lock()
	defer configMu.Unlock()
	if _, err := core.AgentRoleByID(role); err != nil {
		return err
	}
	b, err := os.ReadFile(ConfigPath)
	if err != nil {
		return err
	}
	var c Config
	if err = toml.Unmarshal(b, &c); err != nil {
		return err
	}
	team, _ := c.TeamForBot(parent)
	if team == nil || team.Manager != parent {
		return fmt.Errorf("manager team not found")
	}
	existingTeam, _ := c.TeamForBot(name)
	if existingTeam != nil && existingTeam.ID != team.ID {
		return fmt.Errorf("bot already belongs to another team")
	}
	if name == team.Manager && role != "manager" {
		return fmt.Errorf("cannot remove the team manager role")
	}
	found := false
	for i := range c.Projects {
		p := &c.Projects[i]
		if p.Name != name {
			continue
		}
		found = true
		sameBackend := backend == p.Agent.Type || (backend == "trae" && p.Agent.Type == "acp" && p.Agent.Options["cmd"] == "traecli")
		if backend != "" && !sameBackend {
			opts := map[string]any{"work_dir": p.Agent.Options["work_dir"]}
			switch backend {
			case "codex", "claudecode":
			case "trae":
				backend = "acp"
				opts["cmd"] = "traecli"
				opts["args"] = []string{"acp", "serve"}
			default:
				return fmt.Errorf("unsupported backend; use codex, claudecode or trae")
			}
			p.Agent = AgentConfig{Type: backend, Options: opts}
		}
		if existingTeam == nil || (backend != "" && !sameBackend) {
			UseTeamApprovalDefaults(&p.Agent)
		}
	}
	if !found {
		return fmt.Errorf("bot %q not found", name)
	}
	m := TeamMember{Bot: name, Role: role, Prompt: prompt}
	replaced := false
	for i := range team.Members {
		if team.Members[i].Bot == name {
			m.DisplayName = team.Members[i].DisplayName
			if displayName != nil {
				m.DisplayName = *displayName
			}
			team.Members[i] = m
			replaced = true
		}
	}
	if !replaced {
		if displayName != nil {
			m.DisplayName = *displayName
		}
		team.Members = append(team.Members, m)
	}
	if err = c.validateTeams(); err != nil {
		return err
	}
	return saveConfig(&c)
}
