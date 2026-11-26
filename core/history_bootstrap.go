package core

import (
	"encoding/json"
	"log/slog"
	"strings"
)

// SetHistoryBootstrap controls whether a new agent receives recent cc-connect
// history after switching away from a different agent type.
func (e *Engine) SetHistoryBootstrap(enabled bool, maxEntries, maxTokens int) {
	if maxEntries <= 0 {
		maxEntries = 20
	}
	if maxTokens <= 0 {
		maxTokens = 4000
	}
	e.historyBootstrapMu.Lock()
	e.historyBootstrapEnabled = enabled
	e.historyBootstrapMaxEntries = maxEntries
	e.historyBootstrapMaxTokens = maxTokens
	e.historyBootstrapMu.Unlock()
}

func (e *Engine) historyBootstrapPrompt(session *Session, agentType, prompt string) (string, bool) {
	e.historyBootstrapMu.RLock()
	enabled := e.historyBootstrapEnabled
	maxEntries := e.historyBootstrapMaxEntries
	maxTokens := e.historyBootstrapMaxTokens
	e.historyBootstrapMu.RUnlock()
	if !enabled {
		return prompt, false
	}
	history := session.HistoryForAgentSwitch(agentType)
	if len(history) == 0 {
		return prompt, false
	}
	if len(history) > maxEntries {
		history = history[len(history)-maxEntries:]
	}

	// Retain the newest entries within the budget. A single oversized newest
	// entry is truncated so the bootstrap never grows without bound.
	selected := make([]HistoryEntry, 0, len(history))
	used := 0
	for i := len(history) - 1; i >= 0; i-- {
		entry := history[i]
		cost := estimateTokens([]HistoryEntry{entry})
		if used+cost > maxTokens {
			if len(selected) > 0 {
				break
			}
			runes := []rune(entry.Content)
			capRunes := maxTokens * 4
			if len(runes) > capRunes {
				entry.Content = string(runes[len(runes)-capRunes:])
			}
		}
		selected = append(selected, entry)
		used += cost
	}
	var b strings.Builder
	b.WriteString("[cc-connect prior conversation from this chat]\n")
	for i := len(selected) - 1; i >= 0; i-- {
		line, err := json.Marshal(struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{Role: selected[i].Role, Content: selected[i].Content})
		if err != nil {
			continue
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	b.WriteString("[/cc-connect prior conversation]\n\n")
	slog.Info("prior conversation sent to switched agent", "agent_type", agentType, "entries", len(selected))
	return b.String() + prompt, true
}
