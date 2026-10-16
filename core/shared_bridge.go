package core

import (
	"encoding/json"
	"strings"
)

// Strip bridge-only instructions from daemon history before showing it to a
// user. The first turn may also have a configured Codex preamble before this
// context block; local CC Connect history already stores the original input.
func StripSharedBridgePrompt(text string) string {
	start := strings.Index(text, "[cc-connect invocation context]\n")
	if start < 0 {
		return text
	}
	end := strings.Index(text[start:], "[/cc-connect invocation context]\n")
	if end < 0 {
		return text
	}
	contextLine := strings.SplitN(text[start+len("[cc-connect invocation context]\n"):], "\n", 2)[0]
	var values map[string]string
	if json.Unmarshal([]byte(contextLine), &values) != nil || values["project"] == "" || values["session_key"] == "" {
		return text
	}
	text = text[start+end+len("[/cc-connect invocation context]\n"):]
	if strings.HasPrefix(text, "[cc-connect sender_id=") {
		if newline := strings.IndexByte(text, '\n'); newline >= 0 {
			text = text[newline+1:]
		}
	}
	return text
}
