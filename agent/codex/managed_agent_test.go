package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

func TestManagedBusyMessageModeConfigAndWorkspaceClone(t *testing.T) {
	for _, mode := range []string{"queue", "steer"} {
		a := fixtureManagedAgent(t, "/socket", t.TempDir(), map[string]any{"daemon_busy_message_mode": mode})
		if a.AutoSteerBusyMessages() != (mode == "steer") {
			t.Fatalf("incorrect policy for %s", mode)
		}
		clone, err := New(a.WorkspaceAgentOptions())
		if err != nil {
			t.Fatal(err)
		}
		if clone.(*managedAgent).daemon.busyMessageMode != mode {
			t.Fatal("workspace clone lost busy message mode")
		}
	}
	defaults, err := parseManagedOptions(nil)
	if err != nil || defaults.busyMessageMode != "queue" {
		t.Fatalf("default mode changed: %+v %v", defaults, err)
	}
	for _, opts := range []map[string]any{
		{"daemon_busy_message_mode": "invalid"},
		{"daemon_busy_message_mode": true},
		{"daemon_busy_message_mode": "steer", "daemon_enable_steer": false},
	} {
		if _, err := parseManagedOptions(opts); err == nil {
			t.Fatalf("invalid configuration accepted: %v", opts)
		}
	}
}

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

func TestManagedProviderCatalogEditsPreserveSelectionByName(t *testing.T) {
	a := fixtureManagedAgent(t, "/unused", t.TempDir(), nil)
	a.SetProviders([]core.ProviderConfig{{Name: "one"}, {Name: "two"}})
	a.SetActiveProvider("two")
	a.SetProviders([]core.ProviderConfig{{Name: "two"}})
	if p := a.GetActiveProvider(); p == nil || p.Name != "two" {
		t.Fatal("removing earlier provider lost active default")
	}
	a.SetProviders([]core.ProviderConfig{{Name: "three"}})
	if a.GetActiveProvider() != nil {
		t.Fatal("removed provider silently selected a different provider")
	}
}

func TestManagedSnapshotScopeRejectsOtherCwdAndCurrentThread(t *testing.T) {
	for _, raw := range []string{"null", "true", "[]", "1.2"} {
		if _, err := managedRequestID(json.RawMessage(raw)); err == nil {
			t.Errorf("invalid ID accepted: %s", raw)
		}
	}
	id, err := managedRequestID(json.RawMessage(`"42"`))
	if err != nil || id != "s:42" {
		t.Fatal("numeric string collapsed into number")
	}
	cwd := t.TempDir()
	socket := managedFixture(t, func(ws *websocket.Conn) {
		fixtureRPC(t, ws, func(m daemonMessage) (any, bool) {
			if m.Method == "thread/resume" {
				return fixtureSnapshot(t.TempDir(), "target", ""), true
			}
			return nil, false
		})
	})
	a := fixtureManagedAgent(t, socket, cwd, nil)
	if _, err := a.AttachSession(context.Background(), "target"); err == nil {
		t.Fatal("other workspace attached")
	}
	t.Setenv("CODEX_THREAD_ID", "self")
	if _, err := a.AttachSession(context.Background(), "self"); err == nil {
		t.Fatal("current development thread attached")
	}
}

func TestManagedTransportConfigIsSeparateAndOptIn(t *testing.T) {
	binary := os.Args[0] // Constructor lookup must not launch this executable.
	for _, backend := range []string{"exec", "app_server"} {
		agent, err := New(map[string]any{"cmd": binary, "backend": backend})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := agent.(core.AgentSessionAttacher); ok {
			t.Fatalf("legacy %s gained daemon capabilities", backend)
		}
		if _, ok := agent.(core.DefaultSettingsAgent); ok {
			t.Fatalf("legacy %s gained managed settings semantics", backend)
		}
		if agent.(*Agent).appServerTransport != "stdio" {
			t.Fatal("legacy default changed")
		}
	}
	for _, opts := range []map[string]any{
		{"app_server_transport": "managed_daemon"},
		{"backend": "app_server", "app_server_transport": "unknown"},
		{"backend": "app_server", "app_server_transport": true},
		{"backend": "app_server", "daemon_socket": "/socket"},
		{"backend": "app_server", "daemon_busy_message_mode": "steer"},
		{"backend": "app_server", "app_server_transport": "managed_daemon", "daemon_socket": "/socket", "app_server_url": "stdio://"},
		{"backend": "app_server", "app_server_transport": "managed_daemon", "daemon_socket": "/socket", "env": map[string]any{"A": "B"}},
		{"backend": "app_server", "app_server_transport": "managed_daemon", "daemon_socket": "/socket", "daemon_enable_steer": "yes"},
		{"backend": "app_server", "app_server_transport": "managed_daemon", "daemon_socket": "/socket", "daemon_enable_ster": true},
	} {
		opts["cmd"] = binary
		if _, err := New(opts); err == nil {
			t.Fatalf("invalid mixed configuration accepted: %v", opts)
		}
	}
	a := fixtureManagedAgent(t, "/socket", t.TempDir(), map[string]any{"daemon_attach_only": true, "daemon_enable_steer": false})
	copied := a.WorkspaceAgentOptions()
	clone, err := New(copied)
	if err != nil {
		t.Fatal(err)
	}
	if got := clone.(*managedAgent).daemon; !got.attachOnly || got.steer || got.socket != "/socket" {
		t.Fatalf("workspace lost transport settings: %+v", got)
	}
	if _, err := a.StartSession(context.Background(), ""); err == nil {
		t.Fatal("attach-only created a thread")
	}
}

func TestDaemonDiscoveryIsPassive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	binary := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\n[ \"$*\" = \"app-server daemon version\" ] || exit 99\nprintf '%s' '{\"status\":\"running\",\"socketPath\":\"/reported/socket\"}'\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	socket, err := discoverDaemon(context.Background(), binary, nil, nil)
	if err != nil || socket != "/reported/socket" {
		t.Fatalf("discovery %q %v", socket, err)
	}
}
