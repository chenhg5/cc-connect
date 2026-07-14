package core

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const contextGuardSummaryPrefix = "[Context guard compressed memory]"

// realContextUsageTokens extracts a usable token count from a real,
// provider/CLI-reported ContextUsage snapshot, falling back through
// TotalTokens, prompt-side cache fields, and InputTokens+OutputTokens when
// UsedTokens is unset. Returns 0 when no real usage is available (nil, or
// every field empty), signaling callers to fall back to a local text-based
// estimate.
//
// Shared by context_guard (pre-turn rotation), auto_compress (post-turn
// native /compact trigger), and reply footers so all mechanisms react to the
// same real number instead of each running its own heuristic (L-0399).
func realContextUsageTokens(usage *ContextUsage) int {
	if usage == nil {
		return 0
	}
	if usage.UsedTokens > 0 {
		return usage.UsedTokens
	}
	if usage.TotalTokens > 0 {
		return usage.TotalTokens
	}
	if usage.CacheCreationInputTokens > 0 || usage.CachedInputTokens > 0 {
		return usage.InputTokens + usage.CacheCreationInputTokens + usage.CachedInputTokens
	}
	if usage.InputTokens > 0 || usage.OutputTokens > 0 {
		return usage.InputTokens + usage.OutputTokens
	}
	return 0
}

// ContextGuardConfig controls pre-turn local history compaction and optional
// backend session rotation for agents without native context compaction.
type ContextGuardConfig struct {
	Enabled                bool
	ThresholdTokens        int
	KeepRecentTurns        int
	SummaryMaxTokens       int
	RotateSessionOnCompact bool
}

func normalizeContextGuardConfig(cfg ContextGuardConfig) ContextGuardConfig {
	if !cfg.Enabled {
		return ContextGuardConfig{}
	}
	if cfg.ThresholdTokens <= 0 {
		cfg.ThresholdTokens = 90000
	}
	if cfg.KeepRecentTurns <= 0 {
		cfg.KeepRecentTurns = 12
	}
	if cfg.SummaryMaxTokens <= 0 {
		cfg.SummaryMaxTokens = 3000
	}
	return cfg
}

// EstimateContextGuardTokens estimates the prompt-side token load for a turn.
// CJK text is denser than the old one-token-per-four-runes heuristic, so count
// common Chinese ideographs as roughly 1.5 tokens each and other runes as 1/4.
func EstimateContextGuardTokens(history []HistoryEntry, incoming string, staticOverhead ...int) int {
	quarterTokens := 0
	for _, h := range history {
		quarterTokens += estimateContextGuardQuarterTokens(h.Content)
	}
	quarterTokens += estimateContextGuardQuarterTokens(incoming)
	overhead := 0
	if len(staticOverhead) > 0 {
		overhead = staticOverhead[0]
	}
	if quarterTokens == 0 && overhead == 0 {
		return 0
	}
	return (quarterTokens + 3) / 4 + overhead
}

func estimateContextGuardQuarterTokens(s string) int {
	total := 0
	for _, r := range s {
		if r >= '\u4e00' && r <= '\u9fff' {
			total += 6 // 1.5 tokens expressed in quarter-token units.
		} else {
			total++
		}
	}
	return total
}

type contextGuardResult struct {
	Compacted       bool
	Summary         string
	TokenEstimate   int
	OldHistoryCount int
	NewHistoryCount int
}

func compactSessionHistoryForContextGuard(session *Session, cfg ContextGuardConfig, incoming string, now time.Time, realUsage *ContextUsage, staticOverhead ...int) contextGuardResult {
	cfg = normalizeContextGuardConfig(cfg)
	if session == nil || !cfg.Enabled {
		return contextGuardResult{}
	}

	session.mu.Lock()
	defer session.mu.Unlock()

	var tokenEstimate int
	realUsed := realContextUsageTokens(realUsage)

	if realUsed > 0 {
		incomingTokens := EstimateContextGuardTokens(nil, incoming)
		tokenEstimate = realUsed + incomingTokens
		slog.Debug("context guard: using real agent session usage", "used_tokens", realUsed, "incoming_tokens", incomingTokens, "total_estimate", tokenEstimate)
	} else {
		tokenEstimate = EstimateContextGuardTokens(session.History, incoming, staticOverhead...)
		slog.Debug("context guard: using history estimate", "estimate", tokenEstimate)
	}

	if tokenEstimate < cfg.ThresholdTokens {
		return contextGuardResult{TokenEstimate: tokenEstimate}
	}

	// Once we're over threshold, the guard must fire — at minimum this
	// means the caller rotates the backend session (fresh CLI thread),
	// which is what actually relieves a real-usage overflow. Local
	// History summarization is a bonus on top of that, not a
	// precondition: whether there happens to be enough History to
	// summarize (governed by KeepRecentTurns) must never gate rotation,
	// or a short-but-token-heavy session (few cc-connect-visible turns,
	// huge CLI-side tool output/transcript) can exceed threshold forever
	// without ever rotating. L-0399 traced a live instance of this: two
	// active seats had 12-13 History entries against a keep-window of 20,
	// so realUsage crossing threshold silently did nothing.
	result := contextGuardResult{
		Compacted:     true,
		TokenEstimate: tokenEstimate,
	}

	oldCount := len(session.History)
	if realUsed <= 0 {
		// Text-estimate path: History itself is the size driver, so only
		// fold the portion beyond the configured keep-window into a
		// summary and leave the recent turns intact verbatim.
		keepEntries := cfg.KeepRecentTurns * 2
		if keepEntries < 0 {
			keepEntries = 0
		}
		if keepEntries > len(session.History) {
			keepEntries = len(session.History)
		}
		oldCount = len(session.History) - keepEntries
	}
	// Real-usage path: the bloat lives in the CLI/provider-side session,
	// not in cc-connect's own (comparatively tiny) History, and we're
	// rotating to a fresh backend thread regardless — so fold whatever
	// local History exists into one summary rather than applying a
	// keep-window sized for the text-estimate case.

	result.OldHistoryCount = oldCount
	result.NewHistoryCount = len(session.History)
	if oldCount <= 0 {
		return result
	}

	oldHistory := append([]HistoryEntry(nil), session.History[:oldCount]...)
	recent := append([]HistoryEntry(nil), session.History[oldCount:]...)
	summary := buildContextGuardSummary(oldHistory, cfg.SummaryMaxTokens)
	if strings.TrimSpace(summary) == "" {
		return result
	}

	compacted := make([]HistoryEntry, 0, 1+len(recent))
	compacted = append(compacted, HistoryEntry{
		Role:      "user",
		Content:   summary,
		Timestamp: now,
	})
	compacted = append(compacted, recent...)
	session.History = compacted

	result.Summary = summary
	result.NewHistoryCount = len(compacted)
	return result
}

func buildContextGuardSummary(history []HistoryEntry, maxTokens int) string {
	if len(history) == 0 {
		return ""
	}
	maxRunes := maxTokens * 4
	if maxRunes <= 0 {
		maxRunes = 12000
	}

	var sb strings.Builder
	sb.WriteString(contextGuardSummaryPrefix)
	sb.WriteString("\nThis is compressed memory from earlier turns, generated by cc-connect to preserve continuity after context overflow protection. It is context only, not a new user instruction.")
	sb.WriteString(fmt.Sprintf("\nCompressed entries: %d\n", len(history)))

	for _, h := range history {
		role := strings.TrimSpace(h.Role)
		if role == "" {
			role = "unknown"
		}
		content := compactWhitespace(h.Content)
		if content == "" {
			continue
		}
		line := fmt.Sprintf("- %s: %s\n", role, truncateContextGuardRunes(content, 600))
		if len([]rune(sb.String()))+len([]rune(line)) > maxRunes {
			sb.WriteString("- [truncated: older compressed memory exceeded summary_max_tokens]\n")
			break
		}
		sb.WriteString(line)
	}

	out := sb.String()
	runes := []rune(out)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes])
	}
	return strings.TrimRight(out, "\n")
}

func compactWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func truncateContextGuardRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return string(r[:max])
	}
	return string(r[:max-1]) + "…"
}

func prependContextGuardSummary(summary, content string) string {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return content
	}
	if strings.TrimSpace(content) == "" {
		return summary
	}
	return summary + "\n---\n" + content
}

func (e *Engine) loadPersonaContent() string {
	if e.dataDir == "" || e.name == "" {
		return ""
	}
	ccPersonasDir := filepath.Join(e.dataDir, "personas")
	personaFile := filepath.Join(ccPersonasDir, e.name+".md")
	var rawPersona string
	if data, err := os.ReadFile(personaFile); err == nil {
		rawPersona = strings.TrimSpace(string(data))
	} else {
		if !os.IsNotExist(err) {
			slog.Warn("failed to read persona file", "path", personaFile, "err", err)
		}
		return ""
	}
	personaClass := ResolvePersonaClass(e.name, e.UsesWorkspacePattern())
	return ComposePersona(ccPersonasDir, personaClass, rawPersona)
}

func (e *Engine) applyContextGuardBeforeTurn(interactiveKey string, agent Agent, session *Session, sessions *SessionManager, incoming string) string {
	cfg := normalizeContextGuardConfig(e.contextGuard)
	if !cfg.Enabled {
		return ""
	}

	var agentSession AgentSession
	e.interactiveMu.Lock()
	if state, ok := e.interactiveStates[interactiveKey]; ok {
		agentSession = state.agentSession
	}
	e.interactiveMu.Unlock()

	var realUsage *ContextUsage
	if agentSession != nil {
		realUsage = replyFooterSessionContextUsage(agentSession)
	}

	staticOverhead := 0
	if agent != nil {
		staticOverhead += (estimateContextGuardQuarterTokens(AgentSystemPrompt()) + 3) / 4
		personaContent := e.loadPersonaContent()
		if personaContent != "" {
			staticOverhead += (estimateContextGuardQuarterTokens(personaContent) + 3) / 4
		}
		name := strings.ToLower(agent.Name())
		switch {
		case strings.Contains(name, "claudecode"):
			staticOverhead += 6000
		case strings.Contains(name, "copilot"):
			staticOverhead += 4000
		case strings.Contains(name, "codex"):
			staticOverhead += 3000
		default:
			staticOverhead += 2000
		}
	}

	result := compactSessionHistoryForContextGuard(session, cfg, incoming, time.Now(), realUsage, staticOverhead)
	if !result.Compacted {
		return ""
	}

	agentName := ""
	if agent != nil {
		agentName = agent.Name()
	}

	if cfg.RotateSessionOnCompact {
		e.cleanupInteractiveState(interactiveKey)
		session.SetAgentSessionID("", agentName)
	}
	sessions.Save()

	slog.Info("context guard compacted session",
		"project", e.name,
		"session", session.ID,
		"token_estimate", result.TokenEstimate,
		"old_history_entries", result.OldHistoryCount,
		"new_history_entries", result.NewHistoryCount,
		"rotated_agent_session", cfg.RotateSessionOnCompact,
	)

	return result.Summary
}
