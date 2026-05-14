package ymsagent

import "strings"

func parseCCConnectPromptPlatform(prompt string) string {
	trimmed := strings.TrimLeft(prompt, " \t")
	if !strings.HasPrefix(trimmed, "[cc-connect") {
		return ""
	}
	newline := strings.IndexByte(trimmed, '\n')
	if newline < 0 {
		return ""
	}
	headerLine := strings.TrimSpace(trimmed[:newline])
	if !strings.HasSuffix(headerLine, "]") {
		return ""
	}
	headerLine = strings.TrimSuffix(strings.TrimPrefix(headerLine, "[cc-connect"), "]")
	for _, field := range strings.Fields(headerLine) {
		if platform, ok := strings.CutPrefix(field, "platform="); ok {
			return strings.TrimSpace(platform)
		}
	}
	return ""
}

func parsePlatformFromSessionEnv(env []string) string {
	for _, item := range env {
		value, ok := strings.CutPrefix(item, "CC_SESSION_KEY=")
		if !ok {
			continue
		}
		platform, _, ok := strings.Cut(value, ":")
		if ok {
			return strings.TrimSpace(platform)
		}
		return ""
	}
	return ""
}

func stripKeyboardKeysFromYMSHelp(text string) string {
	if !looksLikeYMSHelp(text) {
		return text
	}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "Keys:" {
			break
		}
		out = append(out, line)
	}
	return strings.TrimRight(strings.Join(out, "\n"), " \n")
}

func looksLikeYMSHelp(text string) bool {
	return strings.Contains(text, "yms-rca") &&
		strings.Contains(text, "\nCommands:") &&
		strings.Contains(text, "\nShortcuts:") &&
		strings.Contains(text, "\nKeys:")
}
