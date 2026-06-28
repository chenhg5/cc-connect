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

// aggregateSeatMessages reads all session JSON files under sessionsDir and
// returns the most recent n history entries across all seats, sorted oldest-first.
// If sessionsDir is empty or unreadable, it returns nil gracefully.
func aggregateSeatMessages(sessionsDir string, n int) []aggregatedEntry {
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
		for _, sess := range snap.Sessions {
			for _, entry := range sess.History {
				if entry.Timestamp.IsZero() || entry.Content == "" {
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
		if len(c) > 200 {
			c = c[:200] + "…"
		}
		// strip newlines from content to keep each entry on one line
		c = strings.ReplaceAll(c, "\n", " ")
		sb.WriteString(fmt.Sprintf("%s %s: %s\n", ts, label, c))
	}
	return strings.TrimRight(sb.String(), "\n")
}
