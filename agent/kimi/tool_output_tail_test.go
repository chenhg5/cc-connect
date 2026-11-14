package kimi

import (
	"context"
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
		s := &kimiSession{ctx: context.Background(), events: make(chan core.Event, 4)}
		for _, content := range []any{output, []any{map[string]any{"type": "text", "text": "older block"}, map[string]any{"type": "text", "text": output}}} {
			s.handleTool(map[string]any{"tool_call_id": "shell", "content": content})
			if len(s.events) == 0 {
				t.Fatal("decoder emitted no result")
			}
			events = append(events, <-s.events)
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
