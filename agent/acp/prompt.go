package acp

import "strings"

// ACP has no portable system-instruction field. Use an explicit first-turn
// preamble without changing shared instruction files in the workspace.
func buildPromptPreamble(systemPrompt, appendPrompt string) string {
	var parts []string
	if s := strings.TrimSpace(systemPrompt); s != "" {
		parts = append(parts, "Configured agent instructions:\n"+s)
	}
	if s := strings.TrimSpace(appendPrompt); s != "" {
		parts = append(parts, "Additional agent instructions:\n"+s)
	}
	return strings.Join(parts, "\n\n")
}
