package acp

import (
	"log/slog"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// SetReasoningEffort stores the effort applied to future sessions.
// A value outside the known list is ignored so a bad /reasoning
// command cannot wipe a previous choice. With no list yet, the
// trimmed value is stored and checked again when the session starts.
func (a *Agent) SetReasoningEffort(effort string) {
	raw := strings.TrimSpace(effort)
	if raw == "" {
		a.mu.Lock()
		a.reasoningEffort = ""
		a.mu.Unlock()
		slog.Info("acp: reasoning effort cleared for future sessions")
		return
	}
	canonical, ok := a.matchEffort(raw)
	if !ok {
		slog.Warn("acp: ignoring unknown reasoning_effort", "value", raw)
		return
	}
	a.mu.Lock()
	a.reasoningEffort = canonical
	a.mu.Unlock()
	slog.Info("acp: reasoning effort changed for future sessions", "effort", canonical)
}

// GetReasoningEffort returns the configured effort, or the thought_level
// the latest session reported when nothing is configured.
func (a *Agent) GetReasoningEffort() string {
	if a == nil {
		return ""
	}
	a.mu.RLock()
	effort := strings.TrimSpace(a.reasoningEffort)
	a.mu.RUnlock()
	if effort != "" {
		return effort
	}
	a.modelsMu.RLock()
	defer a.modelsMu.RUnlock()
	return a.lastThoughtLevel
}

// AvailableReasoningEfforts returns the latest session's thought_level
// choices, or FallbackReasoningEfforts when no session has advertised them.
func (a *Agent) AvailableReasoningEfforts() []string {
	if a == nil {
		return nil
	}
	a.modelsMu.RLock()
	values := append([]string(nil), a.thoughtLevelValues...)
	a.modelsMu.RUnlock()
	if len(values) > 0 {
		return values
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if len(a.FallbackReasoningEfforts) == 0 {
		return nil
	}
	return append([]string(nil), a.FallbackReasoningEfforts...)
}

func (a *Agent) matchEffort(raw string) (string, bool) {
	allowed := a.AvailableReasoningEfforts()
	if len(allowed) == 0 {
		return raw, true
	}
	for _, v := range allowed {
		if strings.EqualFold(v, raw) {
			return v, true
		}
	}
	return "", false
}

// SessionModelOptions returns model choices the latest session
// advertised. It does not fall back to the configured list.
func (a *Agent) SessionModelOptions() []core.ModelOption {
	if a == nil {
		return nil
	}
	a.modelsMu.RLock()
	defer a.modelsMu.RUnlock()
	if len(a.cachedModelOptions) == 0 {
		return nil
	}
	return append([]core.ModelOption(nil), a.cachedModelOptions...)
}
