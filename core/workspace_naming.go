package core

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxWorkspaceDirNameBytes = 120

// workspaceDirNameForChannel derives a safe direct-child directory name from a
// channel/chat name while preserving the original display name as much as
// possible for human operators.
func workspaceDirNameForChannel(channelID, channelName string) string {
	candidate := cleanWorkspaceDirFragment(channelName)
	if candidate == "" || candidate == "." || candidate == ".." {
		fallback := cleanWorkspaceDirFragment(channelID)
		if fallback != "" {
			candidate = "channel-" + fallback
		}
	}
	if candidate == "" || candidate == "." || candidate == ".." {
		candidate = "workspace"
	}
	return truncateWorkspaceDirName(candidate, maxWorkspaceDirNameBytes)
}

func cleanWorkspaceDirFragment(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == 0:
			return -1
		case r == '/', r == '\\', r == ':':
			return '-'
		case unicode.IsControl(r):
			return ' '
		default:
			return r
		}
	}, s)
	s = strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	s = strings.Trim(s, ". -_")
	return s
}

func truncateWorkspaceDirName(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	var b strings.Builder
	b.Grow(maxBytes)
	for _, r := range s {
		size := utf8.RuneLen(r)
		if size < 0 || b.Len()+size > maxBytes {
			break
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	out = strings.Trim(out, ". ")
	if out == "" {
		return "workspace"
	}
	return out
}
