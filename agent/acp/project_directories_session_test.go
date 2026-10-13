package acp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectDirectories_ACPUsesNativeSessionRootsOnStartAndLoad(t *testing.T) {
	for _, resume := range []string{"", "existing-session"} {
		primary, extra := t.TempDir(), t.TempDir()
		capture := filepath.Join(t.TempDir(), "rpc.jsonl")
		base, err := New(map[string]any{"cmd": []string{os.Args[0], "-test.run=^TestDirectoryACPHelper$", "--"}, "project_directory_mode": "trae", "work_dir": primary, "mode": "plan", "additional_dirs": []string{extra}, "env": map[string]string{"CC_DIRECTORY_HELPER": capture}})
		if err != nil {
			t.Fatal(err)
		}
		a := base.(*Agent)
		defer a.Stop()
		for _, clear := range []bool{false, true} {
			if clear {
				if err := a.ConfigureProjectDirectories(primary, nil); err != nil {
					t.Fatal(err)
				}
			}
			session, err := a.StartSession(context.Background(), resume)
			if err != nil {
				t.Fatal(err)
			}
			if !clear && session.(*acpSession).CurrentMode() != "plan" {
				t.Fatal("explicit plan mode was not applied")
			}
			session.Close()
		}
		b, err := os.ReadFile(capture)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			var req struct {
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if err := json.Unmarshal([]byte(line), &req); err != nil {
				t.Fatal(err)
			}
			if req.Method != "session/new" && req.Method != "session/load" {
				continue
			}
			expected := "session/new"
			if resume != "" {
				expected = "session/load"
			}
			if req.Method != expected || req.Params["cwd"] != primary {
				t.Fatalf("wrong context: %+v", req)
			}
			payload, _ := json.Marshal(req.Params["additionalDirectories"])
			want := []string{extra}
			if count == 1 {
				want = []string{}
			}
			expectedJSON, _ := json.Marshal(want)
			if string(payload) != string(expectedJSON) {
				t.Fatalf("roots: %s want %s", payload, expectedJSON)
			}
			count++
		}
		if count != 2 {
			t.Fatalf("session requests: %d", count)
		}
	}
}

func TestDirectoryACPHelper(t *testing.T) {
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
		if req.Method == "initialize" {
			result = map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"loadSession": true, "sessionCapabilities": map[string]any{"additionalDirectories": map[string]any{}}}}
			if os.Getenv("CC_DIRECTORY_NO_CAPABILITY") == "1" {
				result = map[string]any{"protocolVersion": 1}
			}
		}
		if req.Method == "session/new" || req.Method == "session/load" {
			result["sessionId"] = "existing-session"
		}
		if encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}) != nil {
			os.Exit(2)
		}
	}
}

func TestProjectDirectories_RejectServerWithoutAdvertisedCapability(t *testing.T) {
	primary, extra := t.TempDir(), t.TempDir()
	s, err := newACPSession(context.Background(), acpSessionConfig{command: os.Args[0], args: []string{"-test.run=^TestDirectoryACPHelper$", "--"}, workDir: primary, additionalDirs: []string{extra}, extraEnv: []string{"CC_DIRECTORY_HELPER=" + filepath.Join(t.TempDir(), "rpc.jsonl"), "CC_DIRECTORY_NO_CAPABILITY=1"}})
	if s != nil {
		s.Close()
		t.Fatal("old server silently accepted project roots")
	}
	if err == nil || !strings.Contains(err.Error(), "additionalDirectories") {
		t.Fatalf("expected actionable capability error: %v", err)
	}
}
