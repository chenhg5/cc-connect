package claudecode

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
)

func TestClaudeToolReadability_PreservesIDsAndOutputTail(t *testing.T) {
	s := &claudeSession{ctx: context.Background(), events: make(chan core.Event, 8)}
	s.handleAssistant(map[string]any{"message": map[string]any{"content": []any{
		map[string]any{"type": "tool_use", "id": "first", "name": "Bash", "input": map[string]any{"command": "echo 'first'"}},
		map[string]any{"type": "tool_use", "id": "second", "name": "Bash", "input": map[string]any{"command": "echo 'second'"}},
	}}})
	s.handleUser(map[string]any{"message": map[string]any{"content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": "second", "content": "START\n" + strings.Repeat("build output\n", 100) + "FAIL", "is_error": true},
		map[string]any{"type": "tool_result", "tool_use_id": "first", "content": "first"},
	}}})
	for _, id := range []string{"first", "second", "second", "first"} {
		event := <-s.events
		if event.ToolCallID != id {
			t.Fatalf("Claude protocol ID lost: %#v", event)
		}
		if event.Type == core.EventToolResult && id == "second" && (!strings.HasSuffix(event.ToolResult, "FAIL") || event.ToolSuccess == nil || *event.ToolSuccess) {
			t.Fatalf("Claude failure output lost: %#v", event)
		}
	}
}

func TestClaudeToolReadability_HonorsBudgetWithOutOfOrderIDs(t *testing.T) {
	s := &claudeSession{
		ctx: context.Background(), events: make(chan core.Event, 4),
		toolResultMaxLen: 173,
	}
	s.handleAssistant(map[string]any{"message": map[string]any{"content": []any{
		map[string]any{"type": "tool_use", "id": "first", "name": "Bash"},
		map[string]any{"type": "tool_use", "id": "second", "name": "Bash"},
	}}})
	s.handleUser(map[string]any{"message": map[string]any{"content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": "second", "content": strings.Repeat("日志😀", 80), "is_error": true},
		map[string]any{"type": "tool_result", "tool_use_id": "first", "content": "short result / preserved tail"},
	}}})
	for _, id := range []string{"first", "second", "second", "first"} {
		select {
		case e := <-s.events:
			if e.ToolCallID != id {
				t.Fatalf("tool call ID = %q, want %q", e.ToolCallID, id)
			}
			if e.Type != core.EventToolResult {
				continue
			}
			if id == "second" {
				if len(e.ToolResult) > s.toolResultMaxLen || !utf8.ValidString(e.ToolResult) || !strings.Contains(e.ToolResult, "truncated") || e.ToolSuccess == nil || *e.ToolSuccess {
					t.Fatalf("invalid bounded failure result: %+v", e)
				}
			} else if e.ToolResult != "short result / preserved tail" {
				t.Fatalf("short tool result changed: %q", e.ToolResult)
			}
		default:
			t.Fatal("missing tool event")
		}
	}
}
