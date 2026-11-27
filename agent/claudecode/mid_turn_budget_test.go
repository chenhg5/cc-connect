package claudecode

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestClaudeSession_MidTurnStillBoundsToolResults(t *testing.T) {
	cs := newMidTurnTestSession(t, time.Hour)
	if err := cs.SendMidTurn("extend the story", "mid-turn", nil, nil); err != nil {
		t.Fatal(err)
	}
	cs.handleReadLoopLine(resultA)
	payload, err := json.Marshal(map[string]any{
		"type": "user",
		"message": map[string]any{
			"content": []any{map[string]any{"type": "tool_result", "content": strings.Repeat("readable data ", 1000)}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cs.handleReadLoopLine(string(payload))
	select {
	case event := <-cs.events:
		// This is an upper bound for both the original and newer default.
		if event.Type != core.EventToolResult || len(event.ToolResult) > 4000 ||
			!strings.HasPrefix(event.ToolResult, "readable data") {
			t.Fatalf("held mid-turn: first event type=%v size=%d", event.Type, len(event.ToolResult))
		}
	default:
		t.Fatal("no tool result forwarded during held turn")
	}
	cs.handleReadLoopLine(initLine)
	cs.handleReadLoopLine(textB)
	cs.handleReadLoopLine(resultB)
	got := results(drain(cs, 30*time.Millisecond))
	if len(got) != 1 || got[0].Content != "STORY\n\nBANANA." || !got[0].Done {
		t.Fatalf("turns were no longer coalesced: %#v", got)
	}
}
