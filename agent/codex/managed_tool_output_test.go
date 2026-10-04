package codex

import (
	"context"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

func TestManagedAsyncTextAndToolOutputRemainDistinct(t *testing.T) {
	cwd := t.TempDir()
	socket := managedFixture(t, func(ws *websocket.Conn) {
		fixtureRPC(t, ws, func(m daemonMessage) (any, bool) {
			if m.Method != "thread/resume" {
				t.Errorf("unexpected method %s", m.Method)
				return nil, false
			}
			_ = ws.WriteJSON(map[string]any{"id": m.ID, "result": fixtureSnapshot(cwd, "target", "turn")})
			_ = ws.WriteJSON(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "target", "turnId": "turn", "item": map[string]any{"type": "agentMessage", "id": "question", "text": "ASYNC QUESTION TEXT", "phase": "commentary", "delivery": "async", "questions": []any{map[string]any{"title": "Choose direction"}}}}})
			_ = ws.WriteJSON(map[string]any{"method": "item/commandExecution/outputDelta", "params": map[string]any{"threadId": "target", "turnId": "turn", "itemId": "command", "delta": "LIVE TOOL OUTPUT"}})
			_ = ws.WriteJSON(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "target", "turnId": "turn", "item": map[string]any{"type": "commandExecution", "id": "command", "command": "inspect", "aggregatedOutput": "FINAL TOOL OUTPUT", "status": "completed", "exitCode": 0}}})
			return nil, false
		})
	})
	as, err := fixtureManagedAgent(t, socket, cwd, nil).AttachSession(context.Background(), "target")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := as.Close(); err != nil {
			t.Errorf("Close cleanup: %v", err)
		}
	}()
	text := awaitManagedEvent(t, as, core.EventText)
	if text.Metadata["delivery"] != "async" || len(text.Questions) != 1 || text.Questions[0].Question != "Choose direction" || text.ItemID != "question" || text.Content != "ASYNC QUESTION TEXT" || text.Metadata["phase"] != "commentary" {
		t.Fatalf("async text lost or classified as tool output: %+v", text)
	}
	delta := awaitManagedEvent(t, as, core.EventToolOutput)
	if delta.ItemID != "command" || delta.TurnID != "turn" || delta.Content != "LIVE TOOL OUTPUT" {
		t.Fatalf("tool delta lost identity: %+v", delta)
	}
	result := awaitManagedEvent(t, as, core.EventToolResult)
	if result.ItemID != "command" || result.ToolResult != "FINAL TOOL OUTPUT" {
		t.Fatalf("final tool result lost identity: %+v", result)
	}
}
