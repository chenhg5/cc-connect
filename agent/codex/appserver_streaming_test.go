package codex

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestAppServerStreaming_InitializeSubscribesToAnswerDeltas(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &lockedWriteCloser{}
	s := &appServerSession{ctx: ctx, stdin: w, pending: make(map[int64]chan rpcResponseEnvelope)}
	done := make(chan error, 1)
	go func() { done <- s.initialize() }()
	var request struct {
		ID     int64 `json:"id"`
		Params struct {
			Capabilities struct {
				OptOut []string `json:"optOutNotificationMethods"`
			} `json:"capabilities"`
		} `json:"params"`
	}
	if err := json.Unmarshal([]byte(waitForWrittenJSONLine(t, w)), &request); err != nil {
		t.Fatal(err)
	}
	for _, method := range request.Params.Capabilities.OptOut {
		if method == "item/agentMessage/delta" {
			t.Error("initialization disables answer deltas")
		}
	}
	s.handleResponse(rpcResponseEnvelope{ID: request.ID, Result: json.RawMessage(`{}`)})
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("initialize did not finish")
	}
	if !strings.Contains(w.String(), `"method":"initialized"`) {
		t.Fatal("missing initialized notification")
	}
}

func TestAppServerStreaming_DeltasBeforeTurnCompletedWithoutDuplicate(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 16), currentTurn: "turn"}
	s.threadID.Store("thread")
	item := map[string]any{"id": "answer", "type": "agentMessage", "phase": "final_answer", "text": ""}
	s.handleNotification("item/started", notificationProbe(t, map[string]any{"threadId": "thread", "turnId": "turn", "item": item}))
	for _, delta := range []string{"你好", "，世界"} {
		s.handleNotification("item/agentMessage/delta", notificationProbe(t, map[string]any{"threadId": "thread", "turnId": "turn", "itemId": "answer", "delta": delta}))
		select {
		case ev := <-s.events:
			if ev.Type != core.EventText || ev.Content != delta {
				t.Fatalf("event=%+v", ev)
			}
		default:
			t.Fatal("delta buffered until completion")
		}
	}
	item["text"] = "你好，世界！"
	for i := 0; i < 2; i++ {
		s.handleNotification("item/completed", notificationProbe(t, map[string]any{"threadId": "thread", "turnId": "turn", "item": item}))
	}
	if ev := <-s.events; ev.Content != "！" || ev.Type != core.EventText {
		t.Fatalf("completion tail=%+v", ev)
	}
	s.handleNotification("turn/completed", notificationProbe(t, map[string]any{"threadId": "thread", "turn": map[string]any{"id": "turn", "status": "completed"}}))
	if ev := <-s.events; ev.Type != core.EventResult || ev.Content != "你好，世界！" {
		t.Fatalf("expected only terminal result, got %+v", ev)
	}
	select {
	case ev := <-s.events:
		t.Fatalf("duplicate event: %+v", ev)
	default:
	}
}

func TestAppServerStreaming_DoesNotLeakCommentaryOrOtherTurns(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 16), currentTurn: "turn"}
	s.threadID.Store("thread")
	s.handleItemStarted(map[string]any{"id": "answer", "type": "agentMessage", "phase": "final_answer"})
	s.handleItemStarted(map[string]any{"id": "commentary", "type": "agentMessage", "phase": "commentary"})
	for _, n := range []map[string]any{
		{"threadId": "child", "turnId": "turn", "itemId": "answer", "delta": "private child"},
		{"threadId": "thread", "turnId": "old", "itemId": "answer", "delta": "stale turn"},
		{"threadId": "thread", "turnId": "turn", "itemId": "commentary", "delta": "tool preamble"},
	} {
		s.handleNotification("item/agentMessage/delta", notificationProbe(t, n))
	}
	select {
	case ev := <-s.events:
		t.Fatalf("unexpected output: %+v", ev)
	default:
	}
	// The old no-phase protocol still uses completed messages as before.
	s.handleItemCompleted(map[string]any{"id": "old", "type": "agentMessage", "text": "legacy answer"})
	s.completeTurn()
	if ev := <-s.events; ev.Type != core.EventText || ev.Content != "legacy answer" {
		t.Fatalf("legacy event=%+v", ev)
	}
}
