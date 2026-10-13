package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectDirectories_StartResumeAndClearBothBackends(t *testing.T) {
	for _, backend := range []string{"exec", "app_server"} {
		for _, resume := range []string{"", "existing-thread"} {
			for _, clear := range []bool{false, true} {
				primary, extra := t.TempDir(), t.TempDir()
				capture := filepath.Join(t.TempDir(), "rpc.jsonl")
				a := &Agent{cmd: os.Args[0], backend: backend, mode: "suggest", appServerURL: "stdio://", activeIdx: -1, cliExtraArgs: []string{"-test.run=^TestDirectoryCodexHelper$", "--"}, configEnv: []string{"CC_DIRECTORY_HELPER=" + capture}}
				if err := a.ConfigureProjectDirectories(primary, []string{extra}); err != nil {
					t.Fatal(err)
				}
				roots := []string{extra}
				if clear {
					roots = []string{}
					if err := a.ConfigureProjectDirectories(primary, nil); err != nil {
						t.Fatal(err)
					}
				}
				session, err := a.StartSession(context.Background(), resume)
				if err != nil {
					t.Fatal(err)
				}
				defer session.Close()
				if backend == "exec" {
					s := session.(*codexSession)
					args := s.buildExecArgs("hello", nil)
					payload, _ := json.Marshal(roots)
					if !strings.Contains(strings.Join(args, " "), "sandbox_workspace_write.writable_roots="+string(payload)) {
						t.Fatalf("roots missing from exec args: %v", args)
					}
					if !strings.Contains(strings.Join(args, " "), `approval_policy="never"`) || s.workDir != primary {
						t.Fatal("exec changed mode/cwd")
					}
					continue
				}
				b, err := os.ReadFile(capture)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
					var req struct {
						Method string         `json:"method"`
						Params map[string]any `json:"params"`
					}
					if err := json.Unmarshal([]byte(line), &req); err != nil {
						t.Fatal(err)
					}
					if req.Method != "thread/start" && req.Method != "thread/resume" {
						continue
					}
					found = true
					expectedMethod := "thread/start"
					if resume != "" {
						expectedMethod = "thread/resume"
					}
					if req.Method != expectedMethod || req.Params["cwd"] != primary || req.Params["approvalPolicy"] != "on-request" || req.Params["sandbox"] != "read-only" {
						t.Fatalf("wrong native context: %+v", req)
					}
					cfg := req.Params["config"].(map[string]any)
					payload, _ := json.Marshal(cfg["sandbox_workspace_write.writable_roots"])
					want, _ := json.Marshal(roots)
					if string(payload) != string(want) {
						t.Fatalf("wrong roots: %s", payload)
					}
					if resume != "" && req.Params["threadId"] != resume {
						t.Fatal("lost history")
					}
				}
				if !found {
					t.Fatal("no native request")
				}
			}
		}
	}
}

func TestDirectoryCodexHelper(t *testing.T) {
	capture := os.Getenv("CC_DIRECTORY_HELPER")
	if capture == "" {
		return
	}
	f, err := os.OpenFile(capture, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(2)
	}
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for {
		var req struct {
			ID     *int64         `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if decoder.Decode(&req) != nil {
			os.Exit(0)
		}
		if req.ID == nil {
			continue
		}
		if json.NewEncoder(f).Encode(req) != nil {
			os.Exit(2)
		}
		result := map[string]any{}
		if req.Method == "thread/start" || req.Method == "thread/resume" {
			result["thread"] = map[string]any{"id": "existing-thread"}
			if policy := os.Getenv("CC_DIRECTORY_SANDBOX_REPLY"); policy != "" {
				result["sandbox"] = json.RawMessage(policy)
			}
		}
		if req.Method == "turn/start" {
			result["turn"] = map[string]any{"id": "test-turn"}
		}
		if encoder.Encode(map[string]any{"id": req.ID, "result": result}) != nil {
			os.Exit(2)
		}
	}
}
