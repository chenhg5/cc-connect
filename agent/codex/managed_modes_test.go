package codex

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

func TestManagedCurrentPermissionMode_ReportsServerRejection(t *testing.T) {
	cwd := t.TempDir()
	socket := managedFixture(t, func(ws *websocket.Conn) {
		fixtureRPC(t, ws, func(m daemonMessage) (any, bool) {
			if m.Method == "thread/resume" {
				return fixtureSnapshot(cwd, "target", ""), true
			}
			if m.Method == "thread/settings/update" {
				if err := ws.WriteJSON(map[string]any{"id": m.ID, "error": map[string]any{"code": -32602, "message": "permissions forbidden by policy"}}); err != nil {
					t.Error(err)
				}
				return nil, false
			}
			t.Errorf("unexpected method: %s", m.Method)
			return map[string]any{}, true
		})
	})
	a := fixtureManagedAgent(t, socket, cwd, nil)
	as, err := a.AttachSession(context.Background(), "target")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := as.Close(); err != nil {
			t.Error(err)
		}
	}()
	err = as.(*managedSession).SetCurrentPermissionMode(context.Background(), "approve-for-me")
	if err == nil || !strings.Contains(err.Error(), "permissions forbidden by policy") {
		t.Fatalf("rejection error=%v", err)
	}
	if a.GetMode() != "default" {
		t.Fatal("rejected update changed creation defaults")
	}
}

func TestManagedCurrentPermissionMode_UpdatesOnlyAttachedThread(t *testing.T) {
	for _, tc := range []struct{ mode, profile, approval, reviewer string }{
		{"ask-for-approval", ":workspace", "on-request", "user"},
		{"approve-for-me", ":workspace", "on-request", "auto_review"},
		{"full-access", ":danger-full-access", "never", "user"},
		{"read-only", ":read-only", "on-request", "user"},
		{"suggest", ":read-only", "on-request", "user"},
		{"auto-edit", ":workspace", "never", "user"},
		{"full-auto", ":workspace", "never", "user"},
		{"yolo", ":danger-full-access", "never", "user"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			cwd := t.TempDir()
			var mu sync.Mutex
			var update map[string]any
			socket := managedFixture(t, func(ws *websocket.Conn) {
				fixtureRPC(t, ws, func(m daemonMessage) (any, bool) {
					mu.Lock()
					defer mu.Unlock()
					switch m.Method {
					case "thread/settings/update":
						if err := json.Unmarshal(m.Params, &update); err != nil {
							t.Error(err)
						}
						return map[string]any{}, true
					case "thread/resume":
						snapshot := fixtureSnapshot(cwd, "target", "")
						if update != nil {
							snapshot["approvalPolicy"] = update["approvalPolicy"]
							snapshot["approvalsReviewer"] = update["approvalsReviewer"]
							snapshot["activePermissionProfile"] = map[string]any{"id": update["permissions"]}
						}
						return snapshot, true
					default:
						t.Errorf("unexpected mutation or RPC: %s", m.Method)
						return nil, false
					}
				})
			})
			a := fixtureManagedAgent(t, socket, cwd, nil)
			as, err := a.AttachSession(context.Background(), "target")
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := as.Close(); err != nil {
					t.Error(err)
				}
			}()
			s := as.(*managedSession)
			if err := s.SetCurrentPermissionMode(context.Background(), tc.mode); err != nil {
				t.Fatal(err)
			}
			settings, err := s.ReadRuntimeSettings(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if settings.PermissionProfile != tc.profile || settings.ApprovalPolicy != tc.approval || settings.ApprovalsReviewer != tc.reviewer {
				t.Fatalf("settings=%+v", settings)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(update) != 4 || update["threadId"] != "target" {
				t.Fatalf("unexpected settings overrides: %v", update)
			}
			if a.GetMode() != "default" || a.newThreadParams()["permissions"] != nil {
				t.Fatal("current permission switch changed creation defaults")
			}
		})
	}
}

func TestManagedCurrentPermissionMode_RejectsBusyAndDefaultWithoutRPC(t *testing.T) {
	a := fixtureManagedAgent(t, "/unused", t.TempDir(), nil)
	s := &managedSession{agent: a, thread: "target", turn: "busy"}
	if err := s.SetCurrentPermissionMode(context.Background(), "full-access"); !errors.Is(err, core.ErrAgentTurnBusy) {
		t.Fatalf("busy error=%v", err)
	}
	s.turn = ""
	s.pending = map[string]*managedRequest{"pending": {}}
	if err := s.SetCurrentPermissionMode(context.Background(), "read-only"); !errors.Is(err, core.ErrAgentTurnBusy) {
		t.Fatalf("pending approval error=%v", err)
	}
	s.pending = nil
	for _, mode := range []string{"default", "", "unknown"} {
		if err := s.SetCurrentPermissionMode(context.Background(), mode); err == nil {
			t.Fatalf("accepted %q", mode)
		}
	}
}

func TestManagedApprovalFlow_PreservesCustomProfile(t *testing.T) {
	cwd := t.TempDir()
	var mu sync.Mutex
	reviewer := "user"
	updates := 0
	socket := managedFixture(t, func(ws *websocket.Conn) {
		fixtureRPC(t, ws, func(m daemonMessage) (any, bool) {
			mu.Lock()
			defer mu.Unlock()
			switch m.Method {
			case "thread/settings/update":
				var params map[string]any
				if err := json.Unmarshal(m.Params, &params); err != nil {
					t.Error(err)
				}
				if len(params) != 3 || params["threadId"] != "target" || params["approvalPolicy"] != "on-request" {
					t.Errorf("approval-only update overrides extra settings: %v", params)
				}
				for _, key := range []string{"permissions", "sandbox", "sandboxPolicy", "config"} {
					if _, exists := params[key]; exists {
						t.Errorf("approval-only update includes %s", key)
					}
				}
				reviewer, _ = params["approvalsReviewer"].(string)
				updates++
				return map[string]any{}, true
			case "thread/resume":
				snapshot := fixtureSnapshot(cwd, "target", "")
				snapshot["activePermissionProfile"] = map[string]any{"id": "custom-network"}
				snapshot["sandbox"] = map[string]any{"type": "workspace-write", "networkAccess": true}
				snapshot["approvalPolicy"], snapshot["approvalsReviewer"] = "on-request", reviewer
				return snapshot, true
			default:
				t.Errorf("unexpected method %s", m.Method)
				return map[string]any{}, true
			}
		})
	})
	a := fixtureManagedAgent(t, socket, cwd, nil)
	as, err := a.AttachSession(context.Background(), "target")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := as.Close(); err != nil {
			t.Error(err)
		}
	}()
	s := as.(*managedSession)
	for _, mode := range []string{"auto-review", "request"} {
		if err := s.SetCurrentApprovalMode(context.Background(), mode); err != nil {
			t.Fatal(err)
		}
		settings, err := s.ReadRuntimeSettings(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		wantReviewer := "user"
		if mode == "auto-review" {
			wantReviewer = "auto_review"
		}
		if settings.PermissionProfile != "custom-network" || settings.Sandbox != "workspace-write" || settings.ApprovalsReviewer != wantReviewer {
			t.Fatalf("settings=%+v", settings)
		}
	}
	if a.GetMode() != "default" || a.GetApprovalMode() != "default" {
		t.Fatal("current approval routing changed new-session defaults")
	}
	mu.Lock()
	defer mu.Unlock()
	if updates != 2 {
		t.Fatalf("updates=%d", updates)
	}
}

func TestManagedApprovalDefaults_PresetPrecedenceAndWorkspaceClone(t *testing.T) {
	a := fixtureManagedAgent(t, "/unused", t.TempDir(), nil)
	a.SetApprovalMode("auto-review")
	params := a.newThreadParams()
	if len(params) != 3 || params["approvalPolicy"] != "on-request" || params["approvalsReviewer"] != "auto_review" {
		t.Fatalf("default profile must remain inherited: %v", params)
	}
	clone, err := New(a.WorkspaceAgentOptions())
	if err != nil {
		t.Fatal(err)
	}
	if clone.(*managedAgent).GetApprovalMode() != "auto-review" {
		t.Fatal("clone lost approval override")
	}
	a.SetMode("full-access")
	if a.GetApprovalMode() != "default" || a.newThreadParams()["approvalPolicy"] != "never" {
		t.Fatal("full preset retained previous approval override")
	}
	a.SetApprovalMode("request")
	params = a.newThreadParams()
	if params["permissions"] != ":danger-full-access" || params["approvalPolicy"] != "on-request" || params["approvalsReviewer"] != "user" {
		t.Fatalf("partial override replaced profile: %v", params)
	}
	a.SetApprovalMode("default")
	if a.GetMode() != "full-access" || a.newThreadParams()["approvalPolicy"] != "never" {
		t.Fatal("clearing approval override changed full preset")
	}
	a.SetApprovalMode("auto-review")
	a.SetMode("default")
	if len(a.newThreadParams()) != 1 {
		t.Fatal("mode default did not clear both permission overrides")
	}
	configured := fixtureManagedAgent(t, "/unused", t.TempDir(), map[string]any{"mode": "read-only", "approval_mode": "auto-review"})
	params = configured.newThreadParams()
	if params["permissions"] != ":read-only" || params["approvalsReviewer"] != "auto_review" {
		t.Fatal("configured approval override not applied after preset")
	}
}

func TestManagedApprovalMode_RejectsBusyOrInvalidWithoutRPC(t *testing.T) {
	a := fixtureManagedAgent(t, "/unused", t.TempDir(), nil)
	s := &managedSession{agent: a, thread: "target", turn: "busy"}
	if err := s.SetCurrentApprovalMode(context.Background(), "auto-review"); !errors.Is(err, core.ErrAgentTurnBusy) {
		t.Fatalf("busy error=%v", err)
	}
	if err := s.SetCurrentApprovalMode(context.Background(), "default"); err == nil {
		t.Fatal("runtime approval reset accepted")
	}
	for _, value := range []any{true, "invalid"} {
		if _, err := New(map[string]any{"backend": "app_server", "app_server_transport": "managed_daemon", "daemon_socket": "/unused", "approval_mode": value}); err == nil {
			t.Fatalf("accepted approval_mode=%v", value)
		}
	}
}

func TestManagedPermissionModes_PreservesLegacyAndTUIPresets(t *testing.T) {
	a := fixtureManagedAgent(t, "/unused", t.TempDir(), nil)
	want := []string{"default", "ask-for-approval", "approve-for-me", "full-access", "read-only", "suggest", "auto-edit", "full-auto", "yolo"}
	modes := a.PermissionModes()
	if len(modes) != len(want) {
		t.Fatalf("modes=%v", modes)
	}
	for i, key := range want {
		if modes[i].Key != key || modes[i].Name == "" || modes[i].NameZh == "" {
			t.Errorf("mode[%d]=%+v, want %s with labels", i, modes[i], key)
		}
		a.SetMode(key)
		if a.GetMode() != key {
			t.Errorf("SetMode(%q) selected %q", key, a.GetMode())
		}
	}
	a.SetMode("")
	if a.GetMode() != "default" {
		t.Fatal("empty mode must select daemon defaults")
	}
	// Owned backends continue to expose only their existing modes.
	if got := a.Agent.PermissionModes(); len(got) != 4 {
		t.Fatalf("owned backend modes changed: %v", got)
	}
}
