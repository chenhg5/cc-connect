package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"
)

// agentListTimeout bounds a single agent enumeration subprocess call
// (`opencode debug agents` on v2, `opencode agent list` on v1). Mirrors the
// timeout used by model discovery.
const agentListTimeout = 10 * time.Second

// AgentMode is the mode reported for an agent by the CLI's agent enumeration.
type AgentMode string

const (
	AgentModePrimary  AgentMode = "primary"
	AgentModeSubagent AgentMode = "subagent"
	AgentModeAll      AgentMode = "all"
)

// AgentInfo describes a single agent reported by the CLI's agent enumeration
// (`opencode debug agents` on OpenCode v2, `opencode agent list` on v1).
type AgentInfo struct {
	Name string
	Mode AgentMode
}

// agentListLineRe matches a non-indented agent header line from the v1
// `opencode agent list` output, e.g. "build (primary)".
var agentListLineRe = regexp.MustCompile(`^(\S+) \((primary|subagent|all)\)$`)

// hiddenAgentNames are opencode internal agents that appear in the v1
// `opencode agent list` output (marked primary) but cannot be selected as a
// main agent. On v2 the same agents are flagged hidden by `debug agents`.
var hiddenAgentNames = map[string]struct{}{
	"compaction": {},
	"title":      {},
	"summary":    {},
}

// agentValidationSkippedPrefix marks a validation outcome where agent
// enumeration failed and the check was skipped. Callers log this as
// "validation skipped" instead of a false "agent invalid" verdict.
const agentValidationSkippedPrefix = "validation skipped: "

// debugAgentEntry mirrors one entry of the JSON array printed by
// `opencode debug agents` (OpenCode v2). Only the fields used for agent
// selection are decoded; the per-agent system prompts and request overrides
// in the same objects are large and irrelevant here.
type debugAgentEntry struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Mode   string `json:"mode"`
	Hidden bool   `json:"hidden"`
}

// listAgents enumerates the agents available to the configured CLI, trying
// the command layout of each OpenCode major version:
//
//   - OpenCode v2: `opencode debug agents` prints a JSON array.
//   - OpenCode v1: `opencode agent list` prints "<name> (<mode>)" headers.
//
// The v2 layout is tried first and the v1 layout is used as a fallback. An
// error is returned when both fail or neither yields parsable entries, so
// callers degrade gracefully instead of misreporting invalid configuration.
// The enumeration is bounded by agentListTimeout.
func listAgents(ctx context.Context, cmd, workDir string) ([]AgentInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, agentListTimeout)
	defer cancel()

	debugAgents, debugErr := listAgentsDebugJSON(ctx, cmd, workDir)
	if debugErr == nil {
		return debugAgents, nil
	}

	listedAgents, listErr := listAgentsText(ctx, cmd, workDir)
	if listErr == nil {
		return listedAgents, nil
	}

	return nil, fmt.Errorf("opencode: debug agents: %v; opencode: agent list: %w", debugErr, listErr)
}

// listAgentsDebugJSON runs `opencode debug agents` (OpenCode v2) in workDir
// and parses its JSON output. OpenCode v2 answers with an empty array on the
// first call for a directory the background service has not loaded yet
// (observed on v2.0.26: a cold directory returns [] once and the full list on
// the immediate second call), so one retry is performed before giving up.
func listAgentsDebugJSON(ctx context.Context, cmd, workDir string) ([]AgentInfo, error) {
	agents, err := runDebugAgentsOnce(ctx, cmd, workDir)
	if err != nil {
		return nil, err
	}
	if len(agents) == 0 {
		agents, err = runDebugAgentsOnce(ctx, cmd, workDir)
		if err != nil {
			return nil, err
		}
	}
	if len(agents) == 0 {
		return nil, fmt.Errorf("opencode: debug agents: no agent entries in output")
	}
	return agents, nil
}

// runDebugAgentsOnce runs a single `opencode debug agents` call and parses
// the JSON output. A valid empty result is returned as an empty slice with a
// nil error so the caller can decide whether to retry.
func runDebugAgentsOnce(ctx context.Context, cmd, workDir string) ([]AgentInfo, error) {
	c := exec.CommandContext(ctx, cmd, "debug", "agents")
	c.Dir = workDir
	out, err := c.Output()
	if err != nil {
		return nil, fmt.Errorf("opencode: debug agents: %w", err)
	}

	return parseDebugAgentsOutput(string(out))
}

// listAgentsText runs `opencode agent list` (OpenCode v1) in workDir and
// parses its text output.
func listAgentsText(ctx context.Context, cmd, workDir string) ([]AgentInfo, error) {
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

// parseDebugAgentsOutput parses the JSON array emitted by `opencode debug
// agents` (v2). Entries flagged hidden, carrying no usable name, or with an
// unknown mode are skipped; the hidden v2 agents (compaction/title/summary/
// goal-verify) are the equivalents of hiddenAgentNames on v1. The agent ID
// is preferred over the display name because `--agent`/SetAgent accept IDs.
func parseDebugAgentsOutput(out string) ([]AgentInfo, error) {
	var entries []debugAgentEntry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		return nil, fmt.Errorf("opencode: debug agents: parse JSON: %w", err)
	}

	agents := make([]AgentInfo, 0, len(entries))
	for _, entry := range entries {
		if entry.Hidden {
			continue
		}
		name := strings.TrimSpace(entry.ID)
		if name == "" {
			name = strings.TrimSpace(entry.Name)
		}
		if name == "" {
			continue
		}
		if _, hidden := hiddenAgentNames[name]; hidden {
			continue
		}
		mode := AgentMode(entry.Mode)
		switch mode {
		case AgentModePrimary, AgentModeSubagent, AgentModeAll:
		default:
			continue
		}
		agents = append(agents, AgentInfo{Name: name, Mode: mode})
	}
	return agents, nil
}

// parseAgentListOutput parses `opencode agent list` output (v1): agent
// header lines are "<name> (<mode>)", followed by an indented permission
// JSON block that is skipped. Lines that do not match the header format are
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
