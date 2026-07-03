package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// formatPendingReactions renders a reaction slice as a one-line context prefix.
// Example: "[Pending reactions: 👎 3min ago, ❤️ 1min ago]"
func formatPendingReactions(reactions []PendingReaction) string {
	var parts []string
	now := time.Now()
	for _, r := range reactions {
		age := now.Sub(r.When).Round(time.Second)
		var ageStr string
		switch {
		case age < time.Minute:
			ageStr = "just now"
		case age < time.Hour:
			ageStr = fmt.Sprintf("%dmin ago", int(age.Minutes()))
		default:
			ageStr = fmt.Sprintf("%dh ago", int(age.Hours()))
		}
		parts = append(parts, r.Emoji+" "+ageStr)
	}
	return "[Pending reactions: " + strings.Join(parts, ", ") + "]"
}

// aggregatedEntry is one history turn collected across all seat sessions.
type aggregatedEntry struct {
	Project   string
	Role      string    // "user" or "assistant"
	Content   string
	Timestamp time.Time
}

// extractSessionChatID extracts the chat-id component from a cc-connect session key.
// Session keys have the form "platform:chatID:..." or "/workspace/path:platform:chatID:...".
// Returns "" if the key cannot be parsed.
func extractSessionChatID(sessionKey string) string {
	// Handle workspace-prefixed keys by finding a known platform prefix.
	for _, pfx := range []string{
		"telegram:", "feishu:", "slack:", "dingtalk:",
		"discord:", "wecom:", "matrix:", "qqbot:",
	} {
		if idx := strings.Index(sessionKey, pfx); idx >= 0 {
			rest := sessionKey[idx+len(pfx):]
			end := strings.Index(rest, ":")
			if end < 0 {
				return rest
			}
			return rest[:end]
		}
	}
	// Fallback for unrecognized platforms: chatID is at index 1 of colon-split.
	parts := strings.SplitN(sessionKey, ":", 3)
	if len(parts) >= 2 {
		return parts[1]
	}
	return ""
}

// aggregateSeatMessages reads session JSON files under sessionsDir and returns
// the most recent n history entries across all seats that belong to chatID,
// sorted oldest-first. Pass chatID="" to disable the filter (not recommended
// in user-facing paths — use only when callers can guarantee privacy).
// If sessionsDir is empty or unreadable it returns nil gracefully.
func aggregateSeatMessages(sessionsDir string, n int, chatID string) []aggregatedEntry {
	if n <= 0 || sessionsDir == "" {
		return nil
	}
	files, err := filepath.Glob(filepath.Join(sessionsDir, "*.json"))
	if err != nil || len(files) == 0 {
		return nil
	}

	var all []aggregatedEntry
	seen := make(map[string]struct{})

	for _, f := range files {
		base := filepath.Base(f)
		// derive project name: "chef-seat_ec780d50.json" → "chef-seat"
		project := strings.TrimSuffix(base, ".json")
		if idx := strings.LastIndex(project, "_"); idx > 0 {
			project = project[:idx]
		}

		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var snap sessionSnapshot
		if err := json.Unmarshal(data, &snap); err != nil {
			continue
		}

		// Build the set of internal session IDs that belong to the target chat.
		// snap.UserSessions maps sessionKey → []internalID.
		// We only include sessions whose sessionKey chat-ID component matches.
		var allowed map[string]struct{}
		if chatID != "" {
			allowed = make(map[string]struct{})
			for sk, ids := range snap.UserSessions {
				if extractSessionChatID(sk) != chatID {
					continue
				}
				for _, id := range ids {
					allowed[id] = struct{}{}
				}
			}
			if len(allowed) == 0 {
				continue // no sessions for this chat in this file
			}
		}

		for id, sess := range snap.Sessions {
			if allowed != nil {
				if _, ok := allowed[id]; !ok {
					continue
				}
			}
			for _, entry := range sess.History {
				if entry.Timestamp.IsZero() || entry.Content == "" {
					continue
				}
				// A stored history entry that is itself a prior group-context
				// injection (starts with the exact prefix formatGroupContext
				// writes) must not be re-aggregated: each seat's own history
				// verbatim-stores whatever was injected into it, so without
				// this guard every subsequent @-mention re-includes the last
				// N messages *including* their nested prior injections,
				// which themselves contain even older ones — compounding
				// without bound over a day of cross-seat activity until the
				// injected block balloons to tens of thousands of characters
				// before a single real word is added. Observed 2026-07-03:
				// a plain "hi" expanded into an 11KB+ deeply-nested block,
				// making seats appear hung when they were just chewing
				// through an enormous accidental prompt.
				if strings.HasPrefix(entry.Content, "[Group context (") {
					continue
				}
				// deduplicate by project+timestamp+role
				key := fmt.Sprintf("%s|%s|%d", project, entry.Role, entry.Timestamp.UnixNano())
				if _, dup := seen[key]; dup {
					continue
				}
				seen[key] = struct{}{}
				all = append(all, aggregatedEntry{
					Project:   project,
					Role:      entry.Role,
					Content:   entry.Content,
					Timestamp: entry.Timestamp,
				})
			}
		}
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].Timestamp.Before(all[j].Timestamp)
	})

	if len(all) > n {
		all = all[len(all)-n:]
	}
	return all
}

// formatGroupContext renders aggregated entries as a terse context block.
func formatGroupContext(entries []aggregatedEntry, n int) string {
	if len(entries) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[Group context (last %d)]\n", n))
	for _, e := range entries {
		ts := e.Timestamp.Format("15:04")
		label := e.Project
		if e.Role == "user" {
			label = "Jay"
		}
		c := e.Content
		if runes := []rune(c); len(runes) > 5000 {
			c = string(runes[:5000]) + "…"
		}
		// strip newlines from content to keep each entry on one line
		c = strings.ReplaceAll(c, "\n", " ")
		sb.WriteString(fmt.Sprintf("%s %s: %s\n", ts, label, c))
	}
	return strings.TrimRight(sb.String(), "\n")
}
