package core

import "strings"

// AgentSessionPrompt belongs to the bridge, not AGENTS.md: a normal terminal
// launch in the same workspace must not inherit messaging-platform commands.
func AgentSessionPrompt(lang Language, systemPrompt, appendPrompt string) string {
	parts := []string{AgentSystemPromptForLang(lang)}
	for _, prompt := range []string{systemPrompt, appendPrompt} {
		if prompt = strings.TrimSpace(prompt); prompt != "" {
			parts = append(parts, prompt)
		}
	}
	return strings.Join(parts, "\n\n")
}
