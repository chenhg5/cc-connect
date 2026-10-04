package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestManagedReconnectRunningTurnRestoresCompletedTextAndDeduplicates(t *testing.T) {
	for _, replayNotifications := range []bool{false, true} {
		for _, preview := range []string{"", "FULL", "WRONG PREFIX"} {
			t.Run(fmt.Sprintf("replay=%t/preview=%s", replayNotifications, preview), func(t *testing.T) {
				a := fixtureManagedAgent(t, "/unused", t.TempDir(), nil)
				s := &managedSession{agent: a, decoder: &appServerSession{}, ctx: context.Background(), thread: "target", turn: "turn", events: make(chan core.Event, 16), seenItems: map[string]bool{}, completed: map[string]bool{}, pending: map[string]*managedRequest{}}
				if preview != "" {
					if err := s.handleAgentTextDelta(json.RawMessage(`{"turnId":"turn","itemId":"answer","delta":"` + preview + `"}`)); err != nil {
						t.Fatal(err)
					}
					<-s.events
				}
				item := map[string]any{"type": "agentMessage", "id": "answer", "phase": "final_answer", "text": "FULL OFFLINE ANSWER"}
				snapshot := managedSnapshot{Thread: managedThread{ID: "target", Turns: []managedTurn{{ID: "turn", Status: "inProgress", Items: []map[string]any{item}}}}}
				if err := s.reconcileReconnect(snapshot, "turn", nil); err != nil {
					t.Fatal(err)
				}
				if err := s.reconcileReconnect(snapshot, "turn", nil); err != nil {
					t.Fatal(err)
				}
				if replayNotifications {
					raw, _ := json.Marshal(map[string]any{"turnId": "turn", "item": item})
					if err := s.handleMessage(daemonMessage{Method: "item/completed", Params: raw}); err != nil {
						t.Fatal(err)
					}
					// A buffered start notification for the same turn must not erase recovery.
					if err := s.handleMessage(daemonMessage{Method: "turn/started", Params: json.RawMessage(`{"threadId":"target","turn":{"id":"turn"}}`)}); err != nil {
						t.Fatal(err)
					}
				}
				if err := s.completeManagedTurn("turn", "completed", "", nil); err != nil {
					t.Fatal(err)
				}
				textCount := 0
				var result core.Event
				for len(s.events) > 0 {
					e := <-s.events
					if e.Type == core.EventText {
						textCount++
						if preview == "WRONG PREFIX" && e.Metadata["replace_item_text"] != true {
							t.Fatal("missing authoritative replacement")
						}
					}
					if e.Type == core.EventResult {
						result = e
					}
				}
				if result.Content != "FULL OFFLINE ANSWER" || textCount != 1 {
					t.Fatalf("lost/duplicate recovery: text=%d result=%+v", textCount, result)
				}
				if s.RuntimeState().TurnID != "" {
					t.Fatal("turn not completed")
				}
			})
		}
	}
}

func TestManagedExpectedInterruptSkipsReplacementTurn(t *testing.T) {
	s := &managedSession{agent: fixtureManagedAgent(t, "/unused", t.TempDir(), nil), ctx: context.Background(), thread: "target", turn: "new"}
	// No RPC connection exists: touching transport would fail this assertion.
	if err := s.CancelExpectedTurn("old"); err != nil {
		t.Fatal(err)
	}
	if s.RuntimeState().TurnID != "new" {
		t.Fatal("replacement was interrupted")
	}
}
