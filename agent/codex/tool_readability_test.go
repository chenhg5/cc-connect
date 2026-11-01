package codex

import (
	"context"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestAppServerToolReadability_PreservesOutputTail(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 8)}
	output := "START\n" + strings.Repeat("building package\n", 100) + "FAIL: missing dependency"
	s.handleItemCompleted(map[string]any{
		"type": "commandExecution", "id": "command-1", "command": "go test ./core",
		"status": "completed", "exitCode": float64(1), "aggregatedOutput": output,
	})
	event := <-s.events
	if event.ToolResult != output {
		t.Fatal("adapter truncated output before the configured display policy")
	}
	if event.Type != core.EventToolResult || !strings.Contains(event.ToolResult, "START") || !strings.Contains(event.ToolResult, "FAIL: missing dependency") {
		t.Fatalf("lost actionable output: %#v", event)
	}
	if event.ToolSuccess == nil || *event.ToolSuccess {
		t.Fatal("nonzero exit must fail")
	}
}

func TestAppServerToolReadability_CorrelatesAllToolKinds(t *testing.T) {
	for _, item := range []map[string]any{
		{"type": "commandExecution", "id": "command", "command": "printf 'hello'", "exitCode": float64(0), "aggregatedOutput": "hello"},
		{"type": "mcpToolCall", "id": "mcp", "server": "docs", "tool": "search", "arguments": map[string]any{"query": "x"}, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "first\nsecond"}}}},
		{"type": "dynamicToolCall", "id": "dynamic", "tool": "custom_tool", "arguments": map[string]any{"path": "a"}, "contentItems": []any{map[string]any{"type": "inputText", "text": "done"}}},
		{"type": "fileChange", "id": "patch", "changes": []any{map[string]any{"path": "a.go"}}},
	} {
		t.Run(item["type"].(string), func(t *testing.T) {
			s := &appServerSession{events: make(chan core.Event, 8)}
			s.handleItemStarted(item)
			item["status"] = "completed"
			s.handleItemCompleted(item)
			start, result := <-s.events, <-s.events
			if start.Type != core.EventToolUse || result.Type != core.EventToolResult || start.ToolCallID != item["id"] || result.ToolCallID != start.ToolCallID || result.ToolName != start.ToolName {
				t.Fatalf("protocol identity lost: %#v %#v", start, result)
			}
			if item["type"] == "mcpToolCall" && result.ToolResult != "first\nsecond" {
				t.Fatalf("transport wrapper shown instead of text: %q", result.ToolResult)
			}
		})
	}
}

func TestAppServerToolReadability_MCPErrorIsNotSuccess(t *testing.T) {
	event, ok := appServerToolResultEvent(map[string]any{"type": "mcpToolCall", "id": "error", "status": "completed", "result": map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": "permission denied"}}}})
	if !ok || event.ToolSuccess == nil || *event.ToolSuccess || event.ToolResult != "permission denied" {
		t.Fatalf("MCP error hidden: %#v", event)
	}
}

func TestExecToolReadability_PreservesIDAndMissingExitCode(t *testing.T) {
	s := &codexSession{ctx: context.Background(), events: make(chan core.Event, 4)}
	item := map[string]any{"type": "command_execution", "id": "exec-command", "command": "echo 'test'", "status": "failed", "aggregated_output": "START\n" + strings.Repeat("building\n", 100) + "\nFAIL"}
	s.handleItemStarted(map[string]any{"item": item})
	s.handleItemCompleted(map[string]any{"item": item})
	start, result := <-s.events, <-s.events
	if start.ToolCallID != "exec-command" || result.ToolCallID != start.ToolCallID || result.ToolExitCode != nil || result.ToolSuccess == nil || *result.ToolSuccess || !strings.HasSuffix(result.ToolResult, "FAIL") {
		t.Fatalf("exec result misleading: %#v", result)
	}
}
