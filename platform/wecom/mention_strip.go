package wecom

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

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
	return stripLeadingDisplayMentionCommand(strings.TrimSpace(s))
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

// stripLeadingDisplayMentionCommand removes any leading @-mentions before a
// slash-command (/) or shell-bang (!) marker. WeCom's WS aibot callback does
// not deliver structured @-mention metadata, so the bot's display name is
// embedded directly in Text.Content. The previous implementation used
// strings.Fields(s)[0] as the mention token, which silently failed when:
//   - the bot's display name contains spaces (e.g. "Claude Code"),
//   - the user @-mentions multiple parties before the command
//     (e.g. "@张三 @机器人 /list"),
//   - or the mention token contains punctuation other than whitespace.
// The fix scans for the first '/' or '!' that appears at a token boundary
// (i.e. preceded by whitespace) and treats everything before it as the
// mention prefix. Matching at a token boundary avoids false positives on
// things like "https://", "1/2", or display names containing those chars.
func stripLeadingDisplayMentionCommand(s string) string {
	if s == "" {
		return s
	}
	if !strings.HasPrefix(s, "@") && !strings.HasPrefix(s, "＠") {
		return s
	}
	for i, r := range s {
		if r != '/' && r != '!' {
			continue
		}
		if i == 0 {
			return s
		}
		prev, _ := utf8.DecodeLastRuneInString(s[:i])
		if unicode.IsSpace(prev) {
			return strings.TrimSpace(s[i:])
		}
	}
	return s
}
