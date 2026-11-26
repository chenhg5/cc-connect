package claudecode

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestToolErrorDebugLogOmitsResultContent(t *testing.T) {
	const secret = "CLAUDE-TOOL-SENTINEL"
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	cs := &claudeSession{ctx: context.Background(), events: make(chan core.Event, 1)}
	cs.handleUser(map[string]any{"message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "is_error": true, "content": secret}}}})
	select {
	case ev := <-cs.events:
		if !strings.Contains(ev.ToolResult, secret) {
			t.Fatal("tool result was lost from the event stream")
		}
	default:
		t.Fatal("tool result event was not sent")
	}
	if strings.Contains(logs.String(), secret) {
		t.Fatal("Claude Code Debug log exposed tool result content")
	}
}
