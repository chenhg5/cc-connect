package opencode

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"
)

// agentListTimeout bounds a single `opencode agent list` subprocess call.
// Mirrors the timeout used by model discovery.
const agentListTimeout = 10 * time.Second

// AgentMode is the mode reported for an agent by `opencode agent list`.
type AgentMode string

const (
	AgentModePrimary  AgentMode = "primary"
	AgentModeSubagent AgentMode = "subagent"
	AgentModeAll      AgentMode = "all"
)

// AgentInfo describes a single agent reported by `opencode agent list`.
type AgentInfo struct {
	Name string
	Mode AgentMode
}

// agentListLineRe matches a non-indented agent header line from
// `opencode agent list` output, e.g. "build (primary)".
var agentListLineRe = regexp.MustCompile(`^(\S+) \((primary|subagent|all)\)$`)

// hiddenAgentNames are opencode internal agents that appear in `opencode
// agent list` (marked primary) but cannot be selected as a main agent.
var hiddenAgentNames = map[string]struct{}{
	"compaction": {},
	"title":      {},
	"summary":    {},
}

// agentValidationSkippedPrefix marks a validation outcome where agent
// enumeration failed and the check was skipped. Callers log this as
// "validation skipped" instead of a false "agent invalid" verdict.
const agentValidationSkippedPrefix = "validation skipped: "

// listAgents runs `opencode agent list` in workDir and parses the output
// into AgentInfo entries. Internal hidden agents (compaction/title/summary)
// are excluded; subagent-mode entries are kept so callers can distinguish
// "exists but is a subagent" from "does not exist". The command is bounded
// by agentListTimeout. An error is returned when the command fails or no
// agent line could be parsed (zero matches), so callers degrade gracefully
// instead of misreporting invalid configuration.
func listAgents(ctx context.Context, cmd, workDir string) ([]AgentInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, agentListTimeout)
	defer cancel()

	c := exec.CommandContext(ctx, cmd, "agent", "list")
	c.Dir = workDir
	out, err := c.Output()
	if err != nil {
		return nil, fmt.Errorf("opencode: agent list: %w", err)
	}

	agents := parseAgentListOutput(string(out))
	if len(agents) == 0 {
		return nil, fmt.Errorf("opencode: agent list: no parsable agent entries in output")
	}
	return agents, nil
}

// parseAgentListOutput parses `opencode agent list` output: agent header
// lines are "<name> (<mode>)", followed by an indented permission JSON
// block that is skipped. Lines that do not match the header format are
// ignored; a non-empty header line list is always returned.
func parseAgentListOutput(out string) []AgentInfo {
	var agents []AgentInfo
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		m := agentListLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if _, hidden := hiddenAgentNames[m[1]]; hidden {
			continue
		}
		agents = append(agents, AgentInfo{Name: m[1], Mode: AgentMode(m[2])})
	}
	return agents
}

// ValidateConfiguredAgent checks whether the configured agent is usable as
// the main agent for the opencode CLI bound to a. An empty configuration is
// skipped (opencode falls back to its default agent).
//
// Return values:
//   - ("", nil): configuration empty or agent valid (primary/all)
//   - (problem, available): agent invalid — problem describes the reason and
//     available lists the usable (non-subagent) agent names
//   - (problem starting with agentValidationSkippedPrefix, nil): agent
//     enumeration failed; the check was skipped and nothing can be concluded
func (a *Agent) ValidateConfiguredAgent(configured string) (problem string, available []string) {
	if strings.TrimSpace(configured) == "" {
		return "", nil
	}

	agents, err := listAgents(context.Background(), a.cmd, a.workDir)
	if err != nil {
		return agentValidationSkippedPrefix + err.Error(), nil
	}

	usable := make([]string, 0, len(agents))
	mode := AgentMode("")
	found := false
	for _, ag := range agents {
		if ag.Mode != AgentModeSubagent {
			usable = append(usable, ag.Name)
		}
		if ag.Name == configured {
			found = true
			mode = ag.Mode
		}
	}
	sort.Strings(usable)

	if !found {
		return fmt.Sprintf("agent %q does not exist", configured), usable
	}
	if mode == AgentModeSubagent {
		return fmt.Sprintf("agent %q is a subagent and cannot be used as the main agent", configured), usable
	}
	return "", nil
}
