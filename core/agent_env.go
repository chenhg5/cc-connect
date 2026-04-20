package core

import "fmt"

// AgentEnvFromOpts extracts environment variable pairs from opts["env"].
// The "env" option is a map of key-value pairs injected into every agent
// process at startup. Supports map[string]string and map[string]any (the
// latter is what TOML parsing produces).
// Returns nil if "env" is not set or empty.
func AgentEnvFromOpts(opts map[string]any) []string {
	raw, ok := opts["env"]
	if !ok || raw == nil {
		return nil
	}
	switch m := raw.(type) {
	case map[string]string:
		var out []string
		for k, v := range m {
			out = append(out, k+"="+v)
		}
		return out
	case map[string]any:
		var out []string
		for k, v := range m {
			out = append(out, fmt.Sprintf("%s=%v", k, v))
		}
		return out
	default:
		return nil
	}
}
