// Package devin integrates Devin CLI (https://cli.devin.ai/) as a
// first-class cc-connect agent.
//
// Devin speaks the Agent Client Protocol (ACP) over stdio via its
// `devin acp` subcommand, so the transport and session plumbing is
// shared with the generic agent/acp package. This package is a thin
// wrapper that:
//
//  1. Registers Devin under the stable config name `type = "devin"`
//     (parallel to `claudecode`, `cursor`, `codex`, etc.), so minimal
//     user config doesn't need to spell out the ACP command / args.
//  2. Pins Devin-specific defaults — binary name "devin", subcommand
//     "acp", human-readable display name "Devin" — while leaving every
//     underlying ACP option (mode, auth_method, env, work_dir, etc.)
//     overridable from project config.
//  3. Reports Name() = "devin" so cc-connect's session store keys,
//     audit logs, and /doctor output attribute activity to Devin
//     rather than to the generic "acp" adapter.
//
// Authentication is delegated entirely to the local Devin CLI: after a
// one-time `devin auth login`, the spawned `devin acp` subprocess
// reads the credentials stored on disk, so cc-connect never needs to
// see or forward any API tokens. Windsurf Enterprise users can
// alternatively inject WINDSURF_API_KEY via the agent env option.
package devin

import (
	"strings"

	"github.com/chenhg5/cc-connect/agent/acp"
	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterAgent("devin", New)
}

// Agent embeds *acp.Agent so it inherits StartSession, ListSessions,
// ModeSwitcher, AgentDoctorInfo, and all other optional capability
// interfaces implemented by the ACP adapter — only Name() is
// overridden so the engine identifies this as a Devin agent.
type Agent struct {
	*acp.Agent
}

// Name returns the stable agent type identifier used in config,
// session store keys, and audit logging.
func (a *Agent) Name() string { return "devin" }

// New builds a Devin agent from project options.
//
// Option handling:
//   - "command" defaults to "devin" (override only if you have the
//     binary at a non-standard path; always use an absolute path when
//     running under systemd / launchd where PATH is minimal).
//   - "args" defaults to ["acp"].
//   - "display_name" defaults to "Devin".
//   - All other ACP options (work_dir, mode, auth_method, env) are
//     passed through unchanged to agent/acp.
func New(opts map[string]any) (core.Agent, error) {
	a, err := acp.New(applyDevinDefaults(opts))
	if err != nil {
		return nil, err
	}
	base, ok := a.(*acp.Agent)
	if !ok {
		// agent/acp.New always returns *acp.Agent today; if the
		// concrete type ever changes, fall through with a plain
		// wrapper rather than panicking.
		return a, nil
	}
	return &Agent{Agent: base}, nil
}

// applyDevinDefaults returns a new opts map with Devin-specific
// defaults filled in for any missing / blank fields. Extracted so
// unit tests can exercise the defaulting logic without requiring
// `devin` to be present in $PATH (which agent/acp.New would check).
func applyDevinDefaults(opts map[string]any) map[string]any {
	if opts == nil {
		opts = make(map[string]any)
	}
	if existing, _ := opts["command"].(string); strings.TrimSpace(existing) == "" {
		opts["command"] = "devin"
	}
	if _, ok := opts["args"]; !ok {
		opts["args"] = []string{"acp"}
	}
	if existing, _ := opts["display_name"].(string); strings.TrimSpace(existing) == "" {
		opts["display_name"] = "Devin"
	}
	return opts
}
