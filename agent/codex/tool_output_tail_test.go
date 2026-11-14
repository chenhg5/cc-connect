package codex

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
		s := &codexSession{ctx: context.Background(), events: make(chan core.Event, 8)}
		for _, item := range []map[string]any{{"type": "command_execution", "aggregated_output": output}, {"type": "function_call", "name": "shell", "output": output}} {
			s.handleItemCompleted(map[string]any{"item": item})
			if len(s.events) == 0 {
				t.Fatal("decoder emitted no result")
			}
			events = append(events, <-s.events)
		}
		app := &appServerSession{ctx: context.Background(), events: make(chan core.Event, 8)}
		for _, item := range []map[string]any{{"type": "commandExecution", "aggregatedOutput": output}, {"type": "mcpToolCall", "tool": "shell", "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": output}}}}, {"type": "dynamicToolCall", "tool": "shell", "contentItems": []any{map[string]any{"type": "inputText", "text": output}}}, {"type": "webSearch", "query": output}} {
			app.handleItemCompleted(item)
			if len(app.events) == 0 {
				t.Fatal("decoder emitted no result")
			}
			events = append(events, <-app.events)
		}
		found := 0
		for _, event := range events {
			if event.Type != core.EventToolResult {
				continue
			}
			found++
			if !strings.Contains(event.ToolResultTail, "FINAL_ERROR_MARKER") || utf8.RuneCountInString(event.ToolResultTail) > 500 || !utf8.ValidString(event.ToolResultTail) {
				t.Fatalf("original output tail lost: %#v", event)
			}
		}
		if found == 0 {
			t.Fatal("no tool result emitted")
		}
	}
}
