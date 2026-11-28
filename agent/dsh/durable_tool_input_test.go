package dsh

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestDurableToolInputWinsOverProvisionalFragments(t *testing.T) {
	s := &dshSession{
		ctx:       context.Background(),
		events:    make(chan core.Event, 8),
		seenCalls: make(map[string]struct{}),
		toolNames: make(map[string]string),
	}
	s.textAgg = newChunkAggregator(40, time.Second, func(string) {})
	s.thinkAgg = newChunkAggregator(40, time.Second, func(string) {})
	t.Cleanup(s.textAgg.Cancel)
	t.Cleanup(s.thinkAgg.Cancel)

	handle := func(frame map[string]any) {
		t.Helper()
		raw, err := json.Marshal(frame)
		if err != nil {
			t.Fatal(err)
		}
		s.handleSessionEvent(raw)
	}
	const id = "tool-01"
	s.handleChunk(&streamChunk{Type: "tool-call-delta", ID: id, Name: "read", ArgsDelt: `{"path":`})
	if len(s.events) != 0 {
		t.Fatal("provisional fragment surfaced as a tool step")
	}
	durable := evToolCall(1, 1, id, "read", `{"path":"sample.txt","limit":20}`)
	handle(durable)
	handle(durable)
	s.handleChunk(&streamChunk{Type: "tool-call-delta", ID: id, Name: "late-fragment", ArgsDelt: "}"})
	handle(evToolResult(1, 1, id, false, "done"))

	if n := len(s.events); n != 2 {
		t.Fatalf("expected exactly one tool use and one tool result, got %d events", n)
	}
	use, result := <-s.events, <-s.events
	if use.Type != core.EventToolUse || use.ToolName != "read" ||
		use.Metadata["tool_use_id"] != id || !json.Valid([]byte(use.ToolInput)) ||
		!strings.Contains(use.ToolInput, `"path":"sample.txt"`) ||
		!strings.Contains(use.ToolInput, `"limit":20`) {
		t.Fatalf("tool use lost complete arguments or call identity: %+v", use)
	}
	if result.Type != core.EventToolResult || result.ToolName != "read" ||
		result.Metadata["tool_use_id"] != id {
		t.Fatalf("tool result lost durable name or call identity: %+v", result)
	}
}
