package codex

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

func TestManagedUsageReadsDaemonWithoutLocalCredentials(t *testing.T) {
	var mu sync.Mutex
	var methods []string
	socket := managedFixture(t, func(ws *websocket.Conn) {
		fixtureRPC(t, ws, func(m daemonMessage) (any, bool) {
			mu.Lock()
			methods = append(methods, m.Method)
			mu.Unlock()
			return map[string]any{"rateLimits": map[string]any{"primary": map[string]any{"usedPercent": 25, "windowDurationMins": 300, "resetsAt": 2000000000}}}, true
		})
	})
	a := fixtureManagedAgent(t, socket, t.TempDir(), map[string]any{"codex_home": t.TempDir()})
	report, err := a.GetUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report == nil || len(report.Buckets) == 0 {
		t.Fatalf("missing daemon report: %+v", report)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(methods) != 1 || methods[0] != "account/rateLimits/read" {
		t.Fatal(methods)
	}
}

func TestManagedDeleteUsesScopedDaemonAPIAndHonorsDisable(t *testing.T) {
	for _, test := range []struct {
		name                                 string
		otherCwd, active, subagent, disabled bool
		wantDelete                           bool
	}{
		{name: "inactive root", wantDelete: true}, {name: "wrong cwd", otherCwd: true}, {name: "active", active: true}, {name: "subagent", subagent: true}, {name: "disabled", disabled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cwd := t.TempDir()
			var mu sync.Mutex
			var methods []string
			socket := managedFixture(t, func(ws *websocket.Conn) {
				fixtureRPC(t, ws, func(m daemonMessage) (any, bool) {
					mu.Lock()
					methods = append(methods, m.Method)
					mu.Unlock()
					var params map[string]any
					_ = json.Unmarshal(m.Params, &params)
					if params["threadId"] != "target" {
						t.Errorf("unscoped request %v", params)
					}
					if m.Method == "thread/read" {
						thread := map[string]any{"id": "target", "cwd": cwd, "turns": []any{}}
						if test.otherCwd {
							thread["cwd"] = cwd + "/other"
						}
						if test.active {
							thread["turns"] = []any{map[string]any{"id": "turn", "status": "inProgress"}}
						}
						if test.subagent {
							thread["source"] = map[string]any{"subagent": map[string]any{}}
						}
						return map[string]any{"thread": thread}, true
					}
					return map[string]any{}, true
				})
			})
			a := fixtureManagedAgent(t, socket, cwd, map[string]any{"daemon_enable_delete": !test.disabled})
			err := a.DeleteSession(context.Background(), "target")
			if (err == nil) != test.wantDelete {
				t.Fatalf("delete error %v", err)
			}
			mu.Lock()
			defer mu.Unlock()
			deleted := false
			for _, method := range methods {
				if method == "thread/delete" {
					deleted = true
				}
			}
			if deleted != test.wantDelete {
				t.Fatalf("methods: %v", methods)
			}
			if test.disabled && len(methods) != 0 {
				t.Fatal("disabled delete connected to daemon")
			}
		})
	}
}

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
	defer as.Close()
	user := awaitManagedEvent(t, as, core.EventUserMessage)
	tool := awaitManagedEvent(t, as, core.EventToolResult)
	if user.Content != "CLI QUESTION" || user.TurnID != "external-turn" || tool.TurnID != "external-turn" || tool.ItemID != "tool" {
		t.Fatalf("scope lost: %+v %+v", user, tool)
	}
}

func TestManagedHistoryPreservesTurnTimestamps(t *testing.T) {
	cwd := t.TempDir()
	const started int64 = 1790934300
	const completed int64 = 1790934365
	socket := managedFixture(t, func(ws *websocket.Conn) {
		fixtureRPC(t, ws, func(m daemonMessage) (any, bool) {
			if m.Method != "thread/read" {
				t.Errorf("unexpected history method %s", m.Method)
			}
			return map[string]any{"thread": map[string]any{"id": "target", "cwd": cwd, "turns": []any{
				map[string]any{"id": "done", "startedAt": started, "completedAt": completed, "items": []any{
					map[string]any{"type": "userMessage", "content": []any{map[string]any{"text": "question"}}},
					map[string]any{"type": "agentMessage", "text": "answer"},
				}},
				map[string]any{"id": "active", "startedAt": completed + 1, "completedAt": nil, "items": []any{map[string]any{"type": "agentMessage", "text": "working"}}},
				map[string]any{"id": "old", "items": []any{map[string]any{"type": "agentMessage", "text": "unknown time"}}},
			}}}, true
		})
	})
	a := fixtureManagedAgent(t, socket, cwd, nil)
	history, err := a.GetSessionHistory(context.Background(), "target", 0)
	if err != nil || len(history) != 4 {
		t.Fatalf("history=%v err=%v", history, err)
	}
	for i, sec := range []int64{started, completed, completed + 1} {
		if !history[i].Timestamp.Equal(time.Unix(sec, 0)) {
			t.Fatalf("item %d time=%v want unix=%d", i, history[i].Timestamp, sec)
		}
	}
	if !history[3].Timestamp.IsZero() {
		t.Fatal("missing timestamp was fabricated")
	}
	limited, err := a.GetSessionHistory(context.Background(), "target", 2)
	if err != nil || len(limited) != 2 || !limited[0].Timestamp.Equal(history[2].Timestamp) {
		t.Fatalf("limited=%v err=%v", limited, err)
	}
}
