package acp

import (
	"encoding/json"
	"github.com/chenhg5/cc-connect/core"
	"strings"
	"testing"
	"unicode/utf8"
)

// Exercise the adapter's real result decoder, including pre-existing summary cuts.
func TestToolResultTail_PreservesOriginalOutput(t *testing.T) {
	for _, prefix := range []string{strings.Repeat("older output\n", 100), strings.Repeat("汉", 600)} {
		output := prefix + "\n最近一行\nFINAL_ERROR_MARKER"
		var events []core.Event
		for _, status := range []string{"completed", "failed", "in_progress", "pending", ""} {
			raw, err := json.Marshal(map[string]any{"sessionId": "session", "update": map[string]any{"sessionUpdate": "tool_call_update", "title": "shell", "status": status, "content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": output}}}}})
			if err != nil {
				t.Fatal(err)
			}
			events = append(events, mapSessionUpdate("session", raw)...)
		}
		found := 0
		for _, event := range events {
			if event.Type != core.EventToolResult {
				continue
			}
			found++
			if !strings.HasSuffix(event.ToolResultTail, "最近一行\nFINAL_ERROR_MARKER") || utf8.RuneCountInString(event.ToolResultTail) > 500 || !utf8.ValidString(event.ToolResultTail) {
				t.Fatalf("original output tail lost: %#v", event)
			}
		}
		if found == 0 {
			t.Fatal("no tool result emitted")
		}
	}
}
