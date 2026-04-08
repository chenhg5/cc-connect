package core

import (
	"sort"
	"strings"
)

// SlashCommandSpec describes a slash-style command exposed by an agent.
type SlashCommandSpec struct {
	Name        string
	Description string
	UsageHint   string
}

// DiscoverAgentNativeGatewayCommands returns native agent commands reachable
// through the /cc gateway, filtered by the current disabled-command policy.
func (e *Engine) DiscoverAgentNativeGatewayCommands() []SlashCommandSpec {
	e.userRolesMu.RLock()
	disabledCmds := e.disabledCmds
	e.userRolesMu.RUnlock()

	return discoverAgentNativeCommands(e.agent, disabledCmds)
}

func discoverAgentNativeCommands(agent Agent, disabled map[string]bool) []SlashCommandSpec {
	ncp, ok := agent.(NativeCommandProvider)
	if !ok {
		return nil
	}
	commands := filterDisabledSlashCommands(append([]SlashCommandSpec(nil), ncp.NativeCommands()...), disabled)
	sortSlashCommandsByName(commands)
	return commands
}

func filterDisabledSlashCommands(commands []SlashCommandSpec, disabled map[string]bool) []SlashCommandSpec {
	if disabled == nil {
		return commands
	}
	filtered := commands[:0]
	for _, cmd := range commands {
		if disabled[strings.ToLower(cmd.Name)] {
			continue
		}
		filtered = append(filtered, cmd)
	}
	return filtered
}

func sortSlashCommandsByName(commands []SlashCommandSpec) {
	sort.Slice(commands, func(i, j int) bool {
		left := strings.ToLower(commands[i].Name)
		right := strings.ToLower(commands[j].Name)
		if left == right {
			return commands[i].Name < commands[j].Name
		}
		return left < right
	})
}

func agentCommandDisplayName(agent Agent) string {
	if agent == nil {
		return ""
	}
	if info, ok := agent.(AgentDoctorInfo); ok && info.CLIDisplayName() != "" {
		return info.CLIDisplayName()
	}
	return agent.Name()
}
