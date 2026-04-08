package wecom

import "strings"

// stripWeComAtMentions removes @<botId> / ＠<botId> segments so group replies like
// "允许 @机器人" still match engine permission keywords (#98). Only affects wecom.
func stripWeComAtMentions(s string, botIDs ...string) string {
	s = strings.TrimSpace(s)
	for _, id := range botIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		s = stripOneWeComAtMention(s, id)
		s = strings.TrimSpace(s)
	}
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return strings.TrimSpace(s)
}

func stripOneWeComAtMention(s, botID string) string {
	if s == "" || botID == "" {
		return s
	}
	// Fullwidth commercial at (common on mobile keyboards)
	s = removeAllEqualFold(s, "＠"+botID)
	// ASCII @
	needleLower := "@" + strings.ToLower(botID)
	for {
		lower := strings.ToLower(s)
		idx := strings.Index(lower, needleLower)
		if idx < 0 {
			return s
		}
		end := idx + len(needleLower)
		if end > len(s) {
			return s
		}
		s = s[:idx] + s[end:]
	}
}

// removeAllEqualFold removes every case-insensitive occurrence of literal sub from s.
// sub must be UTF-8; indices align because case folding does not change byte length
// for ASCII letters in sub.
func removeAllEqualFold(s, sub string) string {
	if sub == "" {
		return s
	}
	subLower := strings.ToLower(sub)
	for {
		lower := strings.ToLower(s)
		idx := strings.Index(lower, subLower)
		if idx < 0 {
			return s
		}
		s = s[:idx] + s[idx+len(sub):]
	}
}
