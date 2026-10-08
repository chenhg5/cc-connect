package codex

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

func TestManagedItemConversionPreservesTurnAndExternalUserInput(t *testing.T) {
	cwd := t.TempDir()
	socket := managedFixture(t, func(ws *websocket.Conn) {
		fixtureRPC(t, ws, func(m daemonMessage) (any, bool) {
			if m.Method == "thread/resume" {
				_ = ws.WriteJSON(map[string]any{"id": m.ID, "result": fixtureSnapshot(cwd, "target", "")})
				for _, item := range []map[string]any{{"id": "user", "type": "userMessage", "content": []any{map[string]any{"type": "text", "text": "CLI QUESTION"}}}, {"id": "tool", "type": "commandExecution", "command": "echo fixture", "status": "completed", "aggregatedOutput": "FIXTURE OUTPUT", "exitCode": 0}} {
					_ = ws.WriteJSON(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "target", "turnId": "external-turn", "item": item}})
				}
				return nil, false
			}
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
	user := awaitManagedEvent(t, as, core.EventUserMessage)
	tool := awaitManagedEvent(t, as, core.EventToolResult)
	if user.Content != "CLI QUESTION" || user.TurnID != "external-turn" || tool.TurnID != "external-turn" || tool.ItemID != "tool" {
		t.Fatalf("scope lost: %+v %+v", user, tool)
	}
}

func TestManagedRequestsPreserveTypedIDsChoicesAndStableQuestionIDs(t *testing.T) {
	cwd := t.TempDir()
	sent := make(chan daemonMessage, 8)
	socket := managedFixture(t, func(ws *websocket.Conn) {
		fixtureRPC(t, ws, func(m daemonMessage) (any, bool) {
			if m.Method == "thread/resume" {
				_ = ws.WriteJSON(map[string]any{"id": m.ID, "result": fixtureSnapshot(cwd, "target", "turn")})
				_ = ws.WriteJSON(map[string]any{"id": 12, "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": "target", "turnId": "turn", "command": "print marker", "availableDecisions": []string{"accept", "cancel"}}})
				_ = ws.WriteJSON(map[string]any{"id": "12", "method": "item/tool/requestUserInput", "params": map[string]any{"threadId": "target", "turnId": "turn", "questions": []any{map[string]any{"id": "a", "question": "Same?", "isOther": true}, map[string]any{"id": "b", "question": "Same?", "isOther": true, "isSecret": true}}}})
				return nil, false
			}
			sent <- m
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
	approval := awaitManagedEvent(t, as, core.EventPermissionRequest)
	question := awaitManagedEvent(t, as, core.EventPermissionRequest)
	if approval.RequestID != "n:12" || question.RequestID != "s:12" || len(approval.Decisions) != 2 {
		t.Fatalf("typed ID/choices lost: %+v %+v", approval, question)
	}
	if err := as.RespondPermission(approval.RequestID, core.PermissionResult{Behavior: "deny"}); err == nil {
		t.Fatal("unoffered decline was sent")
	}
	if err := as.RespondPermission(approval.RequestID, core.PermissionResult{Behavior: "cancel"}); err != nil {
		t.Fatal(err)
	}
	if err := as.RespondPermission(approval.RequestID, core.PermissionResult{Behavior: "allow"}); err == nil {
		t.Fatal("duplicate response accepted")
	}
	if err := as.RespondPermission(question.RequestID, core.PermissionResult{Behavior: "allow", UpdatedInput: map[string]any{"answers": map[string]any{"a": []string{"Other answer"}, "b": []string{"secret answer"}}}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		select {
		case reply := <-sent:
			if i == 0 && string(reply.ID) != "12" {
				t.Fatal("numeric ID changed")
			}
			if i == 1 && string(reply.ID) != `"12"` {
				t.Fatal("string ID changed")
			}
			if i == 1 && (!strings.Contains(string(reply.Result), `"a"`) || !strings.Contains(string(reply.Result), `"b"`)) {
				t.Fatal("stable question IDs lost")
			}
		case <-time.After(time.Second):
			t.Fatal("reply missing")
		}
	}
}

func TestManagedSteerInterruptLiveOutputAndExternalResolution(t *testing.T) {
	cwd := t.TempDir()
	socket := managedFixture(t, func(ws *websocket.Conn) {
		fixtureRPC(t, ws, func(m daemonMessage) (any, bool) {
			switch m.Method {
			case "thread/resume":
				_ = ws.WriteJSON(map[string]any{"id": m.ID, "result": fixtureSnapshot(cwd, "target", "turn")})
				_ = ws.WriteJSON(map[string]any{"id": 9, "method": "item/fileChange/requestApproval", "params": map[string]any{"threadId": "target", "turnId": "turn"}})
				_ = ws.WriteJSON(map[string]any{"method": "serverRequest/resolved", "params": map[string]any{"threadId": "other", "requestId": 9}})
				_ = ws.WriteJSON(map[string]any{"method": "serverRequest/resolved", "params": map[string]any{"threadId": "target", "requestId": 9}})
				_ = ws.WriteJSON(map[string]any{"method": "item/commandExecution/outputDelta", "params": map[string]any{"threadId": "target", "turnId": "turn", "itemId": "command", "delta": "tick\n"}})
				return nil, false
			case "turn/steer":
				var params map[string]any
				_ = json.Unmarshal(m.Params, &params)
				if params["expectedTurnId"] != "turn" || params["threadId"] != "target" {
					t.Errorf("unscoped steer: %s", m.Params)
				}
				return map[string]any{"turnId": "turn"}, true
			case "turn/interrupt":
				var params map[string]any
				_ = json.Unmarshal(m.Params, &params)
				if params["turnId"] != "turn" || params["threadId"] != "target" {
					t.Errorf("unscoped interrupt: %s", m.Params)
				}
				_ = ws.WriteJSON(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "target", "turn": map[string]any{"id": "turn", "status": "interrupted"}}})
				return map[string]any{}, true
			default:
				t.Errorf("unexpected method: %s", m.Method)
				return nil, false
			}
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
	awaitManagedEvent(t, as, core.EventPermissionRequest)
	resolved := awaitManagedEvent(t, as, core.EventPermissionResolved)
	if resolved.RequestID != "n:9" {
		t.Fatal("wrong request resolved")
	}
	output := awaitManagedEvent(t, as, core.EventToolOutput)
	if output.ItemID != "command" || output.Content != "tick\n" {
		t.Fatal("live output lost")
	}
	if err := as.RespondPermission("n:9", core.PermissionResult{Behavior: "allow"}); err == nil {
		t.Fatal("externally resolved approval stayed pending")
	}
	steerer := as.(core.AgentSessionSteerer)
	if err := steerer.Steer("old", "no"); err == nil {
		t.Fatal("stale steer accepted")
	}
	if err := steerer.Steer("turn", "adjust"); err != nil {
		t.Fatal(err)
	}
	if err := as.(core.AgentSessionCanceller).CancelTurn(); err != nil {
		t.Fatal(err)
	}
	final := awaitManagedEvent(t, as, core.EventResult)
	if final.Metadata["turn_status"] != "interrupted" || final.TurnID != "turn" {
		t.Fatalf("wrong completion: %+v", final)
	}
	if as.(core.SharedAgentSession).RuntimeState().TurnID != "" || !as.Alive() {
		t.Fatal("interrupt destroyed session or left active turn")
	}
}

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
