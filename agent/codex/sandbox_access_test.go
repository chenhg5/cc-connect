package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSandboxAccess_PreservesRuntimeRootsAcrossProjectChanges(t *testing.T) {
	for _, backend := range []string{"exec", "app_server"} {
		for _, resume := range []string{"", "existing-thread"} {
			primary, extra, cache := t.TempDir(), t.TempDir(), t.TempDir()
			capture := filepath.Join(t.TempDir(), "rpc.jsonl")
			opts := map[string]any{"cmd": []string{os.Args[0], "-test.run=^TestDirectoryCodexHelper$", "--"}, "backend": backend, "work_dir": primary,
				"mode": "full-auto", "sandbox_writable_roots": []string{cache}, "sandbox_network_access": true,
				"env": map[string]string{"CC_DIRECTORY_HELPER": capture}}
			created, err := New(opts)
			if err != nil {
				t.Fatal(err)
			}
			a := created.(*Agent)
			if err := a.ConfigureProjectDirectories(primary, []string{extra}); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(a.WorkspaceAgentOptions()["sandbox_writable_roots"], []string{cache}) {
				t.Fatal("workspace clone lost runtime access roots")
			}
			if err := a.ConfigureProjectDirectories(primary, nil); err != nil {
				t.Fatal(err)
			}
			session, err := a.StartSession(context.Background(), resume)
			if err != nil {
				t.Fatal(err)
			}
			if backend == "exec" {
				s := session.(*codexSession)
				args := strings.Join(append(append([]string{}, s.cliExtraArgs...), s.buildExecArgs("test", nil)...), " ")
				if !strings.Contains(args, cache) || strings.Contains(args, extra) || !strings.Contains(args, "sandbox_workspace_write.network_access=true") {
					t.Fatalf("incorrect sandbox args: %s", args)
				}
			} else {
				s := session.(*appServerSession)
				if !strings.Contains(strings.Join(s.cliExtraArgs, " "), "sandbox_workspace_write.network_access=true") {
					t.Fatal("network config not forwarded")
				}
				b, _ := os.ReadFile(capture)
				found := false
				for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
					var req struct {
						Method string
						Params map[string]any
					}
					if err := json.Unmarshal([]byte(line), &req); err != nil {
						t.Fatal(err)
					}
					if req.Method != "thread/start" && req.Method != "thread/resume" {
						continue
					}
					found = true
					roots := req.Params["config"].(map[string]any)["sandbox_workspace_write.writable_roots"]
					if req.Params["config"].(map[string]any)["sandbox_workspace_write.network_access"] != true {
						t.Fatal("thread configuration lost explicit network access")
					}
					if !reflect.DeepEqual(roots, []any{cache}) || req.Params["sandbox"] != "workspace-write" || req.Params["approvalPolicy"] != "never" {
						t.Fatalf("wrong sandbox policy: %#v", req.Params)
					}
					if resume != "" && req.Params["threadId"] != resume {
						t.Fatal("lost resume ID")
					}
				}
				if !found {
					t.Fatal("missing thread request")
				}
			}
			if err := session.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestSandboxAccess_EachTurnUsesResolvedPolicyAfterStartOrResume(t *testing.T) {
	allowed, denied := true, false
	for _, tc := range []struct {
		name, mode, policy string
		network            *bool
	}{
		{"allowed", "full-auto", `{"type":"workspaceWrite","writableRoots":["/work"],"networkAccess":true,"excludeTmpdirEnvVar":true,"excludeSlashTmp":true,"readOnlyAccess":{"type":"restricted","includePlatformDefaults":false,"readableRoots":["/work"]}}`, &allowed},
		{"denied", "full-auto", `{"type":"workspaceWrite","writableRoots":[],"networkAccess":false}`, &denied},
		{"server_restricted", "full-auto", `{"type":"workspaceWrite","writableRoots":[],"networkAccess":false}`, &allowed},
		{"readonly", "suggest", `{"type":"readOnly","networkAccess":false,"access":{"type":"restricted","includePlatformDefaults":false,"readableRoots":["/work"]}}`, nil},
		{"inherited", "full-auto", `{"type":"workspaceWrite","writableRoots":["/configured-root"],"networkAccess":true}`, nil},
		{"legacy_omitted", "suggest", "", nil},
		{"legacy_null", "suggest", "null", nil},
	} {
		for _, resume := range []string{"", "existing-thread"} {
			t.Run(fmt.Sprintf("%s/resume=%t", tc.name, resume != ""), func(t *testing.T) {
				capture := filepath.Join(t.TempDir(), "rpc.jsonl")
				opts := map[string]any{
					"cmd":     []string{os.Args[0], "-test.run=^TestDirectoryCodexHelper$", "--"},
					"backend": "app_server", "mode": tc.mode, "work_dir": t.TempDir(),
					"env": map[string]string{"CC_DIRECTORY_HELPER": capture, "CC_DIRECTORY_SANDBOX_REPLY": tc.policy},
				}
				if tc.network != nil {
					opts["sandbox_network_access"] = *tc.network
				}
				a, err := New(opts)
				if err != nil {
					t.Fatal(err)
				}
				s, err := a.StartSession(context.Background(), resume)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				for _, prompt := range []string{"check status", "retry status"} {
					if err := s.Send(prompt, "message", nil, nil); err != nil {
						t.Fatal(err)
					}
				}
				data, err := os.ReadFile(capture)
				if err != nil {
					t.Fatal(err)
				}
				var want any
				if tc.policy != "" {
					if err := json.Unmarshal([]byte(tc.policy), &want); err != nil {
						t.Fatal(err)
					}
				}
				turns := 0
				for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
					var req struct {
						Method string         `json:"method"`
						Params map[string]any `json:"params"`
					}
					if err := json.Unmarshal([]byte(line), &req); err != nil {
						t.Fatal(err)
					}
					if req.Method == "thread/start" || req.Method == "thread/resume" {
						cfg, _ := req.Params["config"].(map[string]any)
						got, present := cfg["sandbox_workspace_write.network_access"]
						if tc.network == nil && present || tc.network != nil && got != *tc.network {
							t.Fatalf("explicit network option changed: %v", cfg)
						}
					}
					if req.Method != "turn/start" {
						continue
					}
					turns++
					if !reflect.DeepEqual(req.Params["sandboxPolicy"], want) {
						t.Fatalf("turn policy = %#v, want resolved policy %#v", req.Params["sandboxPolicy"], want)
					}
					approval, _ := appServerModeSettings(tc.mode)
					if req.Params["approvalPolicy"] != approval {
						t.Fatal("turn changed the configured approval policy")
					}
				}
				if turns != 2 {
					t.Fatalf("captured %d turns, want 2", turns)
				}
			})
		}
	}
}
