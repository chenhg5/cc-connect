package core

import (
	"log/slog"
	"strings"
)

// ParseCmdOpts extracts the command and extra args from agent options.
//
// Priority (first non-empty wins):
//  1. "cmd" (canonical field, no warning)
//  2. "cli_path" (deprecated — logs a warning)
//  3. "command" (deprecated — logs a warning)
//  4. defaultBin (fallback when nothing is configured)
//
// Examples:
//
//	"my-cli code -t foo" → cmd="my-cli", extraArgs=["code", "-t", "foo"]
//	"claude"             → cmd="claude", extraArgs=nil
//	"" (default "pi")    → cmd="pi", extraArgs=nil
func ParseCmdOpts(opts map[string]any, defaultBin string) (cmd string, extraArgs []string) {
	if v, ok := opts["cmd"].(string); ok && strings.TrimSpace(v) != "" {
		parts := strings.Fields(v)
		return parts[0], parts[1:]
	}

	if v, ok := opts["cli_path"].(string); ok && strings.TrimSpace(v) != "" {
		slog.Warn("DEPRECATED: 'cli_path' is deprecated, use 'cmd' instead",
			"deprecated_key", "cli_path",
			"new_key", "cmd",
			"value", v)
		parts := strings.Fields(v)
		return parts[0], parts[1:]
	}

	if v, ok := opts["command"].(string); ok && strings.TrimSpace(v) != "" {
		slog.Warn("DEPRECATED: 'command' is deprecated, use 'cmd' instead",
			"deprecated_key", "command",
			"new_key", "cmd",
			"value", v)
		parts := strings.Fields(v)
		return parts[0], parts[1:]
	}

	return defaultBin, nil
}

// ParseConfigEnv parses opts["env"] (set via [projects.agent.options.env]
// in config.toml) into a []string of KEY=VALUE pairs.
//
// Supports both map[string]string and map[string]any (from TOML parser).
// Returns nil when no env is configured.
//
// The returned slice should be stored separately from sessionEnv so that
// SetSessionEnv calls cannot overwrite static config env.
func ParseConfigEnv(opts map[string]any) []string {
	raw, ok := opts["env"]
	if !ok || raw == nil {
		return nil
	}

	switch m := raw.(type) {
	case map[string]string:
		env := make([]string, 0, len(m))
		for k, v := range m {
			env = append(env, k+"="+v)
		}
		return env
	case map[string]any:
		env := make([]string, 0, len(m))
		for k, v := range m {
			if s, ok := v.(string); ok {
				env = append(env, k+"="+s)
			}
		}
		return env
	default:
		return nil
	}
}
