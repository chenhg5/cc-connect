package claudecode

import (
	"context"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestHandleUserEmitsHookRejectedFromStopFeedback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cs := &claudeSession{
		events: make(chan core.Event, 1),
		ctx:    ctx,
	}

	cs.handleUser(map[string]any{
		"type": "user",
		"message": map[string]any{
			"content": []any{
				map[string]any{
					"type": "text",
					"text": "Stop hook feedback:\n测试闸门:请把回复改写成只有 BBB 三个字母",
				},
			},
		},
	})

	select {
	case event := <-cs.events:
		if event.Type != core.EventType("hook_rejected") {
			t.Fatalf("event type = %q, want hook_rejected", event.Type)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Stop hook feedback text was ignored; no hook_rejected event emitted")
	}
}
