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
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

func TestManagedReconnect_ZeroAttemptsClosesObserverWithoutRetry(t *testing.T) {
	cwd := t.TempDir()
	var connections atomic.Int32
	socket := managedFixture(t, func(ws *websocket.Conn) {
		connections.Add(1)
		fixtureRPC(t, ws, func(m daemonMessage) (any, bool) {
			if m.Method != "thread/resume" {
				t.Errorf("unexpected RPC %s", m.Method)
			}
			if err := ws.WriteJSON(map[string]any{"id": m.ID, "result": fixtureSnapshot(cwd, "target", "")}); err != nil {
				t.Error(err)
			}
			_ = ws.Close()
			return nil, false
		})
	})
	as, err := fixtureManagedAgent(t, socket, cwd, map[string]any{"daemon_reconnect_attempts": 0}).AttachSession(context.Background(), "target")
	if err != nil {
		t.Fatalf("zero retries must still permit the initial connection: %v", err)
	}
	s := as.(*managedSession)
	defer func() {
		if err := as.Close(); err != nil {
			t.Errorf("Close cleanup: %v", err)
		}
	}()
	select {
	case <-s.done:
	case <-time.After(4 * time.Second):
		t.Fatal("observer did not close after disconnect with retries disabled")
	}
	if connections.Load() != 1 {
		t.Fatalf("connections=%d, want only the initial connection", connections.Load())
	}
	if s.RuntimeState().Connected || s.Alive() {
		t.Fatal("disconnected observer remains active")
	}
	foundError := false
	for event := range as.Events() {
		if event.Type == core.EventError && event.Error != nil && strings.Contains(event.Error.Error(), "reconnection disabled") {
			foundError = true
		}
	}
	if !foundError {
		t.Fatal("missing terminal disconnection error")
	}
}

func TestManagedSendDoesNotInjectApplicationContextOrOverrideThread(t *testing.T) {
	cwd := t.TempDir()
	paramsCh := make(chan map[string]any, 1)
	socket := managedFixture(t, func(ws *websocket.Conn) {
		fixtureRPC(t, ws, func(m daemonMessage) (any, bool) {
			if m.Method == "thread/resume" {
				return fixtureSnapshot(cwd, "target", ""), true
			}
			if m.Method == "turn/start" {
				var params map[string]any
				_ = json.Unmarshal(m.Params, &params)
				paramsCh <- params
				return map[string]any{"turn": map[string]any{"id": "turn"}}, true
			}
			return map[string]any{}, true
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
	sender := as.(core.AgentTurnSender)
	if _, err := sender.SendTurn("hello", "message", nil, nil); err != nil {
		t.Fatal(err)
	}
	params := <-paramsCh
	if params["input"].([]any)[0].(map[string]any)["text"] != "hello" {
		t.Fatal(params)
	}
	for _, key := range []string{"additionalContext", "developerInstructions", "collaborationMode", "model", "approvalPolicy", "sandboxPolicy"} {
		if _, exists := params[key]; exists {
			t.Fatalf("overrides thread setting: %s", key)
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

func TestManagedRuntimeSettingsQueryDoesNotOverrideSettingsOrActiveTurn(t *testing.T) {
	cwd := t.TempDir()
	var mu sync.Mutex
	reads := 0
	socket := managedFixture(t, func(ws *websocket.Conn) {
		fixtureRPC(t, ws, func(m daemonMessage) (any, bool) {
			if m.Method != "thread/resume" {
				t.Errorf("unexpected control method: %s", m.Method)
				return nil, false
			}
			var params map[string]any
			_ = json.Unmarshal(m.Params, &params)
			mu.Lock()
			reads++
			n := reads
			mu.Unlock()
			if params["threadId"] != "target" || (n == 1 && len(params) != 1) || (n > 1 && (len(params) != 2 || params["excludeTurns"] != true)) {
				t.Errorf("settings query overrides runtime: %s", m.Params)
			}
			snapshot := fixtureSnapshot(cwd, "target", "active")
			if n > 1 {
				snapshot["model"] = "externally-updated-model"
				snapshot["reasoningEffort"] = "high"
			}
			snapshot["modelProvider"] = "server-provider"
			snapshot["approvalPolicy"] = "on-request"
			snapshot["sandbox"] = map[string]any{"type": "read-only"}
			return snapshot, true
		})
	})
	a := fixtureManagedAgent(t, socket, cwd, map[string]any{"model": "future-model", "mode": "yolo"})
	as, err := a.AttachSession(context.Background(), "target")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := as.Close(); err != nil {
			t.Errorf("Close cleanup: %v", err)
		}
	}()
	settings, err := as.(core.AgentRuntimeSettingsReader).ReadRuntimeSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settings.Model != "externally-updated-model" || settings.ReasoningEffort != "high" || settings.Provider != "server-provider" || settings.ApprovalPolicy != "on-request" || settings.Sandbox != "read-only" {
		t.Fatalf("settings not read from server: %+v", settings)
	}
	if as.(core.SharedAgentSession).RuntimeState().TurnID != "active" || !as.Alive() {
		t.Fatal("query changed active runtime")
	}
	if a.GetModel() != "future-model" {
		t.Fatal("reading runtime changed creation defaults")
	}
	if as.(interface{ GetModel() string }).GetModel() != "externally-updated-model" {
		t.Fatal("runtime footer retained stale model after authoritative query")
	}
}

func TestManagedDefaultsConfigureNewThreadWithoutWritingDaemonCredentials(t *testing.T) {
	cwd, home := t.TempDir(), t.TempDir()
	created := make(chan map[string]any, 1)
	socket := managedFixture(t, func(ws *websocket.Conn) {
		fixtureRPC(t, ws, func(m daemonMessage) (any, bool) {
			if m.Method != "thread/start" {
				t.Errorf("unexpected method %s", m.Method)
				return nil, false
			}
			var params map[string]any
			_ = json.Unmarshal(m.Params, &params)
			created <- params
			return fixtureSnapshot(cwd, "new-thread", ""), true
		})
	})
	a := fixtureManagedAgent(t, socket, cwd, map[string]any{"codex_home": home})
	a.SetProviders([]core.ProviderConfig{{Name: "configured-in-daemon", APIKey: "test-secret-not-to-be-written", BaseURL: "https://example.invalid"}})
	a.SetActiveProvider("configured-in-daemon")
	a.SetModel("future-model")
	a.SetReasoningEffort("high")
	a.SetMode("full-auto")
	as, err := a.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := as.Close(); err != nil {
			t.Errorf("Close cleanup: %v", err)
		}
	}()
	params := <-created
	if params["model"] != "future-model" || params["modelProvider"] != "configured-in-daemon" || params["sandbox"] != "workspace-write" || params["approvalPolicy"] != "never" || params["config"].(map[string]any)["model_reasoning_effort"] != "high" {
		t.Fatalf("creation defaults not applied: %v", params)
	}
	for _, name := range []string{"auth.json", "config.toml"} {
		if _, err := os.Stat(filepath.Join(home, name)); !os.IsNotExist(err) {
			t.Fatalf("managed creation wrote %s: %v", name, err)
		}
	}
}

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
