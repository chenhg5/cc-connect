package ymsagent

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	ansiRE       = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	blankLinesRE = regexp.MustCompile(`\n{3,}`)
)

// stripANSI removes CSI ANSI escape sequences (colors, cursor moves).
func stripANSI(s string) string {
	if s == "" {
		return s
	}
	return ansiRE.ReplaceAllString(s, "")
}

// collapseBlankLines compresses runs of 3 or more "\n" into exactly "\n\n".
func collapseBlankLines(s string) string {
	if s == "" {
		return s
	}
	return blankLinesRE.ReplaceAllString(s, "\n\n")
}

// truncStr truncates a string to maxRunes Unicode characters, appending "...".
func truncStr(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	return string([]rune(s)[:maxRunes]) + "..."
}

// truncBytes truncates a byte slice to maxBytes, preserving UTF-8 boundary
// (used for stderr ring-buffering).
func truncBytes(b []byte, maxBytes int) []byte {
	if len(b) <= maxBytes {
		return b
	}
	tail := b[len(b)-maxBytes:]
	// realign to UTF-8 boundary
	for i := 0; i < 4 && i < len(tail); i++ {
		if utf8.RuneStart(tail[i]) {
			return tail[i:]
		}
	}
	return tail
}

// firstNonEmptyLine returns the first non-empty trimmed line of s.
func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}
