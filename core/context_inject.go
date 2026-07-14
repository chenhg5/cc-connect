package core

import (
	"fmt"
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

// StripGroupContext removes any prepended group context block from the user
// message. The old on_mention_context injector that produced these blocks has
// been removed in favour of file-based chat_history.md sync (L-0423); this
// sanitizer is retained so any legacy content persisted in older session files
// is still cleaned when replayed.
func StripGroupContext(content string) string {
	if !strings.HasPrefix(content, "[Group context (last ") {
		return content
	}
	if idx := strings.Index(content, "\n---\n"); idx >= 0 {
		return content[idx+len("\n---\n"):]
	}
	return ""
}
