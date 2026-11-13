package claudecode

import (
	"context"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// Text deltas must reach the engine once; the completed assistant message that
// follows carries the same text and must be skipped.
func TestPartialStreamingDoesNotDuplicateText(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cs := &claudeSession{events: make(chan core.Event, 8), ctx: ctx}

	cs.handleReadLoopLine(`{"type":"stream_event","event":{"type":"message_start","message":{"id":"m1"}}}`)
	cs.handleReadLoopLine(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"hel"}}}`)
	cs.handleReadLoopLine(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"lo"}}}`)
	cs.handleReadLoopLine(`{"type":"assistant","message":{"id":"m1","content":[{"type":"text","text":"hello"}]}}`)

	var texts []string
	for len(cs.events) > 0 {
		evt := <-cs.events
		if evt.Type == core.EventText {
			texts = append(texts, evt.Content)
		}
	}
	if len(texts) != 2 || texts[0] != "hel" || texts[1] != "lo" {
		t.Fatalf("text events = %v, want exactly the two deltas", texts)
	}
}

// Without deltas (older CLI or non-streamed segment) the completed assistant
// message must still be forwarded.
func TestAssistantTextWithoutPartialStreaming(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cs := &claudeSession{events: make(chan core.Event, 4), ctx: ctx}

	cs.handleReadLoopLine(`{"type":"assistant","message":{"id":"m2","content":[{"type":"text","text":"done"}]}}`)

	select {
	case evt := <-cs.events:
		if evt.Type != core.EventText || evt.Content != "done" {
			t.Fatalf("event = %+v, want text done", evt)
		}
	default:
		t.Fatal("no event emitted for assistant text")
	}
}
