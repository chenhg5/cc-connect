package codex

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

func managedFixture(t *testing.T, handler func(*websocket.Conn)) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix sockets")
	}
	dir, err := os.MkdirTemp("", "cc-managed-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "rpc.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rpc" || r.Host != "localhost" {
			t.Errorf("incorrect UDS handshake %s %s", r.Host, r.URL.Path)
		}
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() {
			if err := ws.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				t.Errorf("Close cleanup: %v", err)
			}
		}()
		_ = ws.SetReadDeadline(time.Now().Add(8 * time.Second))
		handler(ws)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return socket
}
func fixtureRPC(t *testing.T, ws *websocket.Conn, fn func(daemonMessage) (any, bool)) {
	t.Helper()
	for {
		var m daemonMessage
		if err := ws.ReadJSON(&m); err != nil {
			return
		}
		if m.Method == "initialize" {
			if err := ws.WriteJSON(map[string]any{"id": m.ID, "result": map[string]any{}}); err != nil {
				t.Error(err)
			}
			continue
		}
		if m.Method == "initialized" {
			continue
		}
		value, reply := fn(m)
		if reply {
			if err := ws.WriteJSON(map[string]any{"id": m.ID, "result": value}); err != nil {
				return
			}
		}
	}
}
func fixtureSnapshot(cwd, thread, turn string) map[string]any {
	turns := []any{}
	if turn != "" {
		turns = append(turns, map[string]any{"id": turn, "status": "inProgress"})
	}
	return map[string]any{"cwd": cwd, "model": "fixture-model", "reasoningEffort": "low", "thread": map[string]any{"id": thread, "cwd": cwd, "source": "vscode", "turns": turns}}
}
func fixtureManagedAgent(t *testing.T, socket, cwd string, extra map[string]any) *managedAgent {
	t.Helper()
	opts := map[string]any{"backend": "app_server", "app_server_transport": "managed_daemon", "daemon_socket": socket, "daemon_reconnect_attempts": 0, "cmd": "/does-not-exist", "work_dir": cwd}
	for key, value := range extra {
		opts[key] = value
	}
	agent, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return agent.(*managedAgent)
}
func awaitManagedEvent(t *testing.T, as core.AgentSession, kind core.EventType) core.Event {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-as.Events():
			if !ok {
				t.Fatal("session ended before expected event")
			}
			if event.Type == kind {
				return event
			}
		case <-timer.C:
			t.Fatalf("missing event %s", kind)
		}
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

func TestManagedAttachDoesNotOverrideExistingSettingsOrRespondOnClose(t *testing.T) {
	cwd := t.TempDir()
	var mu sync.Mutex
	var methods []string
	response := make(chan daemonMessage, 4)
	socket := managedFixture(t, func(ws *websocket.Conn) {
		fixtureRPC(t, ws, func(m daemonMessage) (any, bool) {
			mu.Lock()
			methods = append(methods, m.Method)
			mu.Unlock()
			if m.Method == "thread/resume" {
				var params map[string]any
				_ = json.Unmarshal(m.Params, &params)
				if len(params) != 1 || params["threadId"] != "target" {
					t.Errorf("attach overrides existing thread: %s", m.Params)
				}
				// Interleave a server request with the exact same ID as this client RPC.
				_ = ws.WriteJSON(map[string]any{"id": m.ID, "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": "other", "turnId": "other-turn", "command": "ignore", "availableDecisions": []string{"accept", "cancel"}}})
				return fixtureSnapshot(cwd, "target", "turn"), true
			}
			if m.Method == "" {
				response <- m
			}
			t.Errorf("unexpected mutation on attachment/close: method=%q", m.Method)
			return nil, false
		})
	})
	a := fixtureManagedAgent(t, socket, cwd, map[string]any{"model": "must-not-override", "mode": "yolo", "append_system_prompt": "must-not-inject"})
	as, err := a.AttachSession(context.Background(), "target")
	if err != nil {
		t.Fatal(err)
	}
	if state := as.(core.SharedAgentSession).RuntimeState(); state.TurnID != "turn" {
		t.Fatalf("active turn not recovered: %+v", state)
	}
	if err := as.Send("must-not-start", "", nil, nil); err == nil {
		t.Fatal("attached active task got a new turn")
	}
	if err := as.RespondPermission("n:2", core.PermissionResult{Behavior: "allow"}); err == nil {
		t.Fatal("answered another thread's request")
	}
	if err := as.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-response:
		t.Fatalf("close answered approval: %+v", m)
	default:
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
