package core

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	toolSecretAssignment = regexp.MustCompile(`(?i)(\b[\w-]*(?:token|secret|password|api[_-]?key|authorization|cookie|credential|access[_-]?key)[\w-]*\s*=\s*)("[^"]*"|'[^']*'|[^\s"'` + "`" + `&]+)`)
	toolSecretJSON       = regexp.MustCompile(`(?i)("[^"\\]*(?:token|secret|password|api[_-]?key|authorization|cookie|credential|access[_-]?key)[^"\\]*"\s*:\s*)"(?:[^"\\]|\\.)*"`)
	toolSecretHeader     = regexp.MustCompile(`(?i)(Authorization\s*:\s*(?:Bearer|Basic|Token)\s+)[^\s'"` + "`" + `]+`)
	toolSecretFlag       = regexp.MustCompile(`(?i)(--[\w-]*(?:token|secret|password|api[_-]?key|credential)[\w-]*\s+)("[^"]*"|'[^']*'|[^\s]+)`)
	toolURLCredentials   = regexp.MustCompile(`(https?://)[^/\s@]+@`)
)

// RedactToolText removes credentials before previews are shortened. Otherwise a
// retained output tail can expose the end of a secret whose key was omitted.
func RedactToolText(text string) string {
	text = toolSecretJSON.ReplaceAllString(text, `${1}"[redacted]"`)
	text = toolSecretHeader.ReplaceAllString(text, `${1}[redacted]`)
	text = toolSecretAssignment.ReplaceAllString(text, `${1}[redacted]`)
	text = toolSecretFlag.ReplaceAllString(text, `${1}[redacted]`)
	return toolURLCredentials.ReplaceAllString(text, `${1}[redacted]@`)
}

// TruncateToolOutput redacts credentials and preserves both the context at the
// start and the actionable result at the end, within the requested rune budget.
func TruncateToolOutput(text string, maxRunes int) string {
	text = RedactToolText(text)
	if maxRunes > 0 {
		lines := strings.Split(text, "\n")
		if len(lines) > 8 {
			text = strings.Join(lines[:3], "\n") + "\n…\n" + strings.Join(lines[len(lines)-4:], "\n")
		}
	}
	if maxRunes <= 0 || utf8.RuneCountInString(text) <= maxRunes {
		return text
	}
	runes := []rune(text)
	const marker = "\n…\n"
	if maxRunes <= len([]rune(marker)) {
		return string(runes[:maxRunes])
	}
	remaining := maxRunes - len([]rune(marker))
	head := (remaining + 1) / 2
	return string(runes[:head]) + marker + string(runes[len(runes)-(remaining-head):])
}

// toolInputPreview keeps structured input valid until the platform extracts its
// display fields. Cutting a JSON document first loses command/path parameters.
func toolInputPreview(text string, maxRunes int) string {
	text = RedactToolText(text)
	if maxRunes <= 0 || utf8.RuneCountInString(text) <= maxRunes {
		return text
	}
	var fields map[string]any
	if json.Unmarshal([]byte(text), &fields) != nil || fields == nil {
		return TruncateToolOutput(text, maxRunes)
	}
	for key, value := range fields {
		if s, ok := value.(string); ok {
			fields[key] = TruncateToolOutput(s, maxRunes)
		}
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return TruncateToolOutput(text, maxRunes)
	}
	return string(encoded)
}

// toolCallID also accepts older adapters that already attach protocol IDs to
// event metadata for tracing.
func toolCallID(event Event) string {
	if event.ToolCallID != "" {
		return event.ToolCallID
	}
	id, _ := event.Metadata["tool_use_id"].(string)
	return strings.TrimSpace(id)
}
