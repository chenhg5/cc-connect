package core

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	toolSecretAssignment = regexp.MustCompile(`(?i)(\b[\w-]*(?:token|secret|password|api[_-]?key|authorization|cookie|credential|bearer|session[_-]?id|access[_-]?key)[\w-]*\s*=\s*)("(?:[^"\\]|\\.)*"|'[^']*'|[^\s"'` + "`" + `&]+)`)
	toolSecretJSON       = regexp.MustCompile(`(?i)("[^"\\]*(?:token|secret|password|api[_-]?key|authorization|cookie|credential|bearer|session[_-]?id|access[_-]?key)[^"\\]*"\s*:\s*)"(?:[^"\\]|\\.)*"`)
	toolSecretHeader     = regexp.MustCompile(`(?i)(Authorization\s*:\s*(?:Bearer|Basic|Token)\s+)("(?:[^"\\]|\\.)*"|'[^']*'|[^\s'"` + "`" + `]+)`)
	toolSecretFlag       = regexp.MustCompile(`(?i)(--[\w-]*(?:token|secret|password|api[_-]?key|credential)[\w-]*\s+)("(?:[^"\\]|\\.)*"|'[^']*'|[^\s]+)`)
	toolURLCredentials   = regexp.MustCompile(`(https?://)[^/\s@]+@`)
)

// RedactToolText removes credentials before previews are shortened. Otherwise a
// retained output tail can expose the end of a secret whose key was omitted.
func RedactToolText(text string) string {
	// Decode before applying shell/header patterns: escaped JSON quotes are not
	// the quoting delimiters of the command stored inside the JSON string.
	if json.Valid([]byte(text)) {
		decoder := json.NewDecoder(strings.NewReader(text))
		decoder.UseNumber()
		var value any
		if decoder.Decode(&value) == nil {
			value = redactToolValue(value)
			var encoded []byte
			var err error
			if strings.Contains(text, "\n") {
				encoded, err = json.MarshalIndent(value, "", "  ")
			} else {
				encoded, err = json.Marshal(value)
			}
			if err == nil {
				return string(encoded)
			}
		}
	}
	return redactPlainToolText(text)
}

var toolSecretName = regexp.MustCompile(`(?i)(token|secret|password|api[_-]?key|authorization|cookie|credential|bearer|session[_-]?id|access[_-]?key)`)

func redactToolValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		for key, field := range value {
			if _, isString := field.(string); isString && toolSecretName.MatchString(key) {
				value[key] = "[redacted]"
			} else {
				value[key] = redactToolValue(field)
			}
		}
		return value
	case []any:
		for i := range value {
			value[i] = redactToolValue(value[i])
		}
		return value
	case string:
		return RedactToolText(value)
	default:
		return value
	}
}

func redactPlainToolText(text string) string {
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
	var value any
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	if !json.Valid([]byte(text)) || decoder.Decode(&value) != nil {
		return TruncateToolOutput(text, maxRunes)
	}
	encoded, err := json.Marshal(truncateToolInputValue(value, maxRunes))
	if err != nil {
		return TruncateToolOutput(text, maxRunes)
	}
	return string(encoded)
}

// Preserve JSON containers and scalar types while bounding every text leaf.
func truncateToolInputValue(value any, maxRunes int) any {
	switch value := value.(type) {
	case map[string]any:
		for key, field := range value {
			value[key] = truncateToolInputValue(field, maxRunes)
		}
		return value
	case []any:
		for i := range value {
			value[i] = truncateToolInputValue(value[i], maxRunes)
		}
		return value
	case string:
		return TruncateToolOutput(value, maxRunes)
	default:
		return value
	}
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
