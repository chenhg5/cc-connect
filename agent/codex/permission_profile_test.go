package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestNew_PermissionProfile validates explicit profile configuration and workspace inheritance.
func TestNew_PermissionProfile(t *testing.T) {
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		backend   string
		profile   any
		wantError bool
	}{
		{"named", "app_server", " workspace-test ", false},
		{"exec", "exec", "workspace-test", true},
		{"default backend", "", "workspace-test", true},
		{"empty", "app_server", "", true},
		{"whitespace", "app_server", " ", true},
		{"wrong type", "app_server", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent, err := New(map[string]any{"cmd": bin, "backend": tc.backend, "permission_profile": tc.profile})
			if tc.wantError {
				if err == nil || !strings.Contains(err.Error(), "permission_profile") {
					t.Fatalf("error = %v, want a permission_profile configuration error", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := agent.(*Agent).WorkspaceAgentOptions()["permission_profile"]; got != "workspace-test" {
				t.Fatalf("inherited profile = %#v, want workspace-test", got)
			}
		})
	}
}

// TestAppServerSession_PermissionProfileSelection keeps named profiles exclusive with legacy sandbox settings in every mode.
func TestAppServerSession_PermissionProfileSelection(t *testing.T) {
	for _, mode := range []string{"suggest", "auto-edit", "full-auto", "yolo"} {
		for _, profile := range []string{"", "workspace-test"} {
			t.Run(mode+"/"+profile, func(t *testing.T) {
				s := &appServerSession{mode: mode, permissionProfile: profile}
				params := s.threadRequestParams()
				approval, sandbox := appServerModeSettings(mode)
				if params["approvalPolicy"] != approval {
					t.Fatalf("approval policy = %v, want %s", params["approvalPolicy"], approval)
				}
				if profile == "" {
					if params["sandbox"] != sandbox {
						t.Fatalf("sandbox = %v, want %s", params["sandbox"], sandbox)
					}
					if _, exists := params["permissions"]; exists {
						t.Fatal("legacy mode unexpectedly selects a named profile")
					}
				} else {
					if params["permissions"] != profile {
						t.Fatalf("permissions = %v, want %s", params["permissions"], profile)
					}
					if _, exists := params["sandbox"]; exists {
						t.Fatal("named permissions must not be combined with sandbox")
					}
				}
			})
		}
	}
}

// TestAppServerSession_RejectsUnconfirmedPermissionProfile prevents older or overriding servers from silently weakening the requested policy.
func TestAppServerSession_RejectsUnconfirmedPermissionProfile(t *testing.T) {
	for _, resumeID := range []string{"", "thread-existing"} {
		for _, active := range []string{"workspace-test", "", "different-profile"} {
			t.Run(resumeID+"/"+active, func(t *testing.T) {
				s := newTransportTestSession(t)
				s.permissionProfile = "workspace-test"
				s.stdin = transportTestWriter(func(p []byte) (int, error) {
					var request struct{ ID int64 }
					if err := json.Unmarshal(p, &request); err != nil {
						return 0, err
					}
					result := map[string]any{"thread": map[string]any{"id": "thread-existing"}}
					if active != "" {
						result["activePermissionProfile"] = map[string]any{"id": active}
					}
					raw, err := json.Marshal(result)
					if err != nil {
						return 0, err
					}
					s.handleResponse(rpcResponseEnvelope{ID: request.ID, Result: raw})
					return len(p), nil
				})
				err := s.ensureThread(resumeID)
				if active == "workspace-test" {
					if err != nil {
						t.Fatal(err)
					}
				} else if err == nil || !strings.Contains(err.Error(), "permission profile") {
					t.Fatalf("error = %v, want unconfirmed profile rejection", err)
				}
			})
		}
	}
}

// TestStartSession_PermissionProfile verifies configuration reaches real start and resume RPCs through the spawned CLI.
func TestStartSession_PermissionProfile(t *testing.T) {
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	agent, err := New(map[string]any{
		"cmd":     []string{bin, "-test.run=^TestPermissionProfileCLIProcess$", "--"},
		"backend": "app_server", "work_dir": t.TempDir(),
		"permission_profile": "workspace-test",
		"env":                map[string]string{"CC_PERMISSION_PROFILE_HELPER": "1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, resumeID := range []string{"", "thread-existing"} {
		t.Run(resumeID, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			session, err := agent.StartSession(ctx, resumeID)
			if err != nil {
				t.Fatal(err)
			}
			if err := session.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestPermissionProfileCLIProcess acts as a local protocol fixture and rejects missing or conflicting permission parameters.
func TestPermissionProfileCLIProcess(t *testing.T) {
	if os.Getenv("CC_PERMISSION_PROFILE_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request struct {
			ID     any
			Method string
			Params map[string]any
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(20)
		}
		if request.ID == nil {
			continue
		}
		result := map[string]any{}
		if request.Method == "thread/start" || request.Method == "thread/resume" {
			_, hasSandbox := request.Params["sandbox"]
			if request.Params["permissions"] != "workspace-test" || hasSandbox {
				fmt.Printf("{\"id\":%v,\"error\":{\"code\":-32602,\"message\":\"invalid permission selection\"}}\n", request.ID)
				continue
			}
			result["thread"] = map[string]any{"id": "thread-existing"}
			result["activePermissionProfile"] = map[string]any{"id": "workspace-test"}
		}
		if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"id": request.ID, "result": result}); err != nil {
			os.Exit(21)
		}
	}
	os.Exit(0)
}
