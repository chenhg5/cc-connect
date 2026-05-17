// Package reasonix integrates Reasonix (https://github.com/esengine/DeepSeek-Reasonix)
// as a first-class cc-connect agent.
//
// Reasonix speaks the Agent Client Protocol (ACP) over stdio via its
// `reasonix acp` subcommand. This wrapper pins the default command,
// arguments, and display name while reusing the generic ACP transport.
package reasonix

import (
	"strings"

	"github.com/chenhg5/cc-connect/agent/acp"
	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterAgent("reasonix", New)
}

// Agent embeds *acp.Agent so it inherits StartSession, ListSessions,
// ModeSwitcher, AgentDoctorInfo, and all other ACP capabilities. Only
// Name is overridden so session records and logs identify Reasonix.
type Agent struct {
	*acp.Agent
}

// Name returns the stable agent type identifier used in config,
// session store keys, and audit logging.
func (a *Agent) Name() string { return "reasonix" }

// New builds a Reasonix agent from project options.
//
// Option handling:
//   - "command" defaults to "reasonix".
//   - "args" defaults to ["acp"].
//   - "display_name" defaults to "Reasonix".
//   - All other ACP options (work_dir, mode, auth_method, env) are
//     passed through unchanged to agent/acp.
func New(opts map[string]any) (core.Agent, error) {
	a, err := acp.New(applyReasonixDefaults(opts))
	if err != nil {
		return nil, err
	}
	base, ok := a.(*acp.Agent)
	if !ok {
		return a, nil
	}
	return &Agent{Agent: base}, nil
}

func applyReasonixDefaults(opts map[string]any) map[string]any {
	if opts == nil {
		opts = make(map[string]any)
	}
	if existing, _ := opts["command"].(string); strings.TrimSpace(existing) == "" {
		opts["command"] = "reasonix"
	}
	if _, ok := opts["args"]; !ok {
		opts["args"] = []string{"acp"}
	}
	if existing, _ := opts["display_name"].(string); strings.TrimSpace(existing) == "" {
		opts["display_name"] = "Reasonix"
	}
	return opts
}
