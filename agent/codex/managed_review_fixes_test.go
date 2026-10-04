package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

func TestManagedReconnectRecoversCompletedTurnOnce(t *testing.T) {
	for _, status := range []string{"completed", "failed", "interrupted"} {
		t.Run(status, func(t *testing.T) {
			cwd := t.TempDir()
			var connections atomic.Int32
			socket := managedFixture(t, func(ws *websocket.Conn) {
				connection := connections.Add(1)
				fixtureRPC(t, ws, func(m daemonMessage) (any, bool) {
					if m.Method != "thread/resume" {
						t.Errorf("unexpected RPC %s", m.Method)
						return nil, false
					}
					if connection == 1 {
						_ = ws.WriteJSON(map[string]any{"id": m.ID, "result": fixtureSnapshot(cwd, "target", "old-turn")})
						_ = ws.Close()
						return nil, false
					}
					snapshot := fixtureSnapshot(cwd, "target", "new-turn")
					turn := map[string]any{"id": "old-turn", "status": status, "items": []any{map[string]any{"id": "final", "type": "agentMessage", "phase": "final_answer", "text": "RECOVERED FINAL"}}}
					if status == "failed" {
						turn["error"] = map[string]any{"message": "FAILED WHILE DISCONNECTED"}
					}
					thread := snapshot["thread"].(map[string]any)
					thread["turns"] = append([]any{turn}, thread["turns"].([]any)...)
					_ = ws.WriteJSON(map[string]any{"id": m.ID, "result": snapshot})
					// A buffered terminal notification after resume must not duplicate recovery.
					_ = ws.WriteJSON(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "target", "turn": turn}})
					_ = ws.WriteJSON(map[string]any{"method": "item/agentMessage/delta", "params": map[string]any{"threadId": "target", "turnId": "new-turn", "itemId": "new-answer", "delta": "NEW TURN LIVE"}})
					return nil, false
				})
			})
			as, err := fixtureManagedAgent(t, socket, cwd, map[string]any{"daemon_reconnect_attempts": 1}).AttachSession(context.Background(), "target")
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := as.Close(); err != nil {
					t.Errorf("Close cleanup: %v", err)
				}
			}()
			var result core.Event
			results := 0
			for {
				event := awaitManagedAnyEvent(t, as)
				if event.Type == core.EventResult {
					result = event
					results++
				}
				if event.Type == core.EventText && event.Content == "NEW TURN LIVE" {
					break
				}
			}
			if results != 1 || result.TurnID != "old-turn" || result.Content != "RECOVERED FINAL" || result.Metadata["turn_status"] != status {
				t.Fatalf("lost/duplicate terminal turn: count=%d %+v", results, result)
			}
			if as.(core.SharedAgentSession).RuntimeState().TurnID != "new-turn" {
				t.Fatal("recovery cleared newer active turn")
			}
		})
	}
}

func awaitManagedAnyEvent(t *testing.T, as core.AgentSession) core.Event {
	t.Helper()
	// Reuse the existing bounded transport assertion without filtering event types.
	select {
	case event, ok := <-as.Events():
		if !ok {
			t.Fatal("observer closed")
		}
		return event
	case <-time.After(4 * time.Second):
		t.Fatal("missing transport event")
	}
	return core.Event{}
}

func TestManagedAsyncQuestionSwitchIsIndependent(t *testing.T) {
	for _, blocking := range []bool{false, true} {
		for _, async := range []bool{false, true} {
			t.Run(fmt.Sprintf("blocking=%t/async=%t", blocking, async), func(t *testing.T) {
				a := fixtureManagedAgent(t, "/socket", t.TempDir(), map[string]any{"daemon_enable_questions": blocking, "daemon_enable_async_questions": async})
				clone, err := New(a.WorkspaceAgentOptions())
				if err != nil {
					t.Fatal(err)
				}
				if clone.(*managedAgent).daemon.questions != blocking || clone.(*managedAgent).daemon.asyncQuestions != async {
					t.Fatal("workspace clone lost independent switches")
				}
				s := &managedSession{agent: a, ctx: context.Background(), thread: "target", events: make(chan core.Event, 4), seenItems: map[string]bool{}, completed: map[string]bool{}}
				err = s.handleMessage(daemonMessage{Method: "item/completed", Params: json.RawMessage(`{"threadId":"target","turnId":"turn","item":{"type":"agentMessage","id":"async","text":"QUESTION TEXT PRESERVED","phase":"commentary","delivery":"async","questions":[{"title":"Preference?","options":["yes","no"]}]}}`)})
				if err != nil {
					t.Fatal(err)
				}
				event := <-s.events
				if event.Content != "QUESTION TEXT PRESERVED" || (len(event.Questions) > 0) != async {
					t.Fatalf("wrong async gate: %+v", event)
				}
			})
		}
	}
	if _, err := parseManagedOptions(map[string]any{"daemon_enable_async_questions": "false"}); err == nil {
		t.Fatal("invalid async flag type accepted")
	}
	defaults, err := parseManagedOptions(nil)
	if err != nil || !defaults.asyncQuestions {
		t.Fatal("default compatibility changed")
	}
}

func TestManagedReconnectRecoversWithoutActiveTurn(t *testing.T) {
	for _, historyRead := range []bool{false, true} {
		t.Run(fmt.Sprintf("historyRead=%t", historyRead), func(t *testing.T) {
			cwd := t.TempDir()
			var connections, reads atomic.Int32
			snapshot := fixtureSnapshot(cwd, "target", "")
			snapshot["thread"].(map[string]any)["turns"] = []any{map[string]any{"id": "old", "status": "completed", "items": []any{map[string]any{"type": "agentMessage", "text": "OFFLINE ANSWER", "phase": "final_answer"}}}}
			socket := managedFixture(t, func(ws *websocket.Conn) {
				connection := connections.Add(1)
				fixtureRPC(t, ws, func(m daemonMessage) (any, bool) {
					switch m.Method {
					case "thread/resume":
						if connection == 1 {
							_ = ws.WriteJSON(map[string]any{"id": m.ID, "result": fixtureSnapshot(cwd, "target", "old")})
							_ = ws.Close()
							return nil, false
						}
						if historyRead {
							return fixtureSnapshot(cwd, "target", ""), true
						}
						return snapshot, true
					case "thread/read":
						reads.Add(1)
						var params map[string]any
						_ = json.Unmarshal(m.Params, &params)
						if params["includeTurns"] != true || params["threadId"] != "target" {
							t.Errorf("wrong history query: %s", m.Params)
						}
						return map[string]any{"thread": snapshot["thread"]}, true
					default:
						t.Errorf("unexpected RPC %s", m.Method)
						return nil, false
					}
				})
			})
			as, err := fixtureManagedAgent(t, socket, cwd, map[string]any{"daemon_reconnect_attempts": 1}).AttachSession(context.Background(), "target")
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := as.Close(); err != nil {
					t.Errorf("Close cleanup: %v", err)
				}
			}()
			result := awaitManagedEvent(t, as, core.EventResult)
			if result.Content != "OFFLINE ANSWER" || result.TurnID != "old" || !result.Done {
				t.Fatalf("lost completion: %+v", result)
			}
			if as.(core.SharedAgentSession).RuntimeState().TurnID != "" {
				t.Fatal("completed turn still active")
			}
			expectedReads := int32(0)
			if historyRead {
				expectedReads = 1
			}
			if reads.Load() != expectedReads {
				t.Fatalf("unexpected history reads: %d", reads.Load())
			}
		})
	}
}

func TestManagedReconnectDoesNotGuessUnfinishedTurnState(t *testing.T) {
	cwd := t.TempDir()
	t.Run("still running", func(t *testing.T) {
		s := &managedSession{ctx: context.Background(), completed: map[string]bool{}, events: make(chan core.Event, 4)}
		snapshot := managedSnapshot{Thread: managedThread{ID: "target", Turns: []managedTurn{{ID: "old", Status: "inProgress"}}}}
		if err := s.reconcileReconnect(snapshot, "old", nil); err != nil {
			t.Fatal(err)
		}
		select {
		case e := <-s.events:
			t.Fatalf("fabricated completion: %+v", e)
		default:
		}
	})
	t.Run("missing from history", func(t *testing.T) {
		var connections atomic.Int32
		socket := managedFixture(t, func(ws *websocket.Conn) {
			connection := connections.Add(1)
			fixtureRPC(t, ws, func(m daemonMessage) (any, bool) {
				switch m.Method {
				case "thread/resume":
					if connection == 1 {
						_ = ws.WriteJSON(map[string]any{"id": m.ID, "result": fixtureSnapshot(cwd, "target", "old")})
						_ = ws.Close()
						return nil, false
					}
					return fixtureSnapshot(cwd, "target", ""), true
				case "thread/read":
					return map[string]any{"thread": fixtureSnapshot(cwd, "target", "")["thread"]}, true
				default:
					t.Errorf("unexpected RPC %s", m.Method)
					return nil, false
				}
			})
		})
		as, err := fixtureManagedAgent(t, socket, cwd, map[string]any{"daemon_reconnect_attempts": 1}).AttachSession(context.Background(), "target")
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := as.Close(); err != nil {
				t.Errorf("Close cleanup: %v", err)
			}
		}()
		event := awaitManagedEvent(t, as, core.EventError)
		if event.Error == nil {
			t.Fatal("lost turn was silently accepted")
		}
		if as.(core.SharedAgentSession).RuntimeState().Connected {
			t.Fatal("failed reconciliation left observer connected")
		}
	})
}
