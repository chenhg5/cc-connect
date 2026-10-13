package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTraeNativeSessionName_MetadataConnectionDoesNotStartTurn(t *testing.T) {
	for _, resume := range []string{"", "existing-thread"} {
		dir := t.TempDir()
		bin := filepath.Join(dir, "traecli")
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(exe, bin); err != nil {
			t.Fatal(err)
		}
		audit := filepath.Join(dir, "rpc.jsonl")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s, err := newACPSession(ctx, acpSessionConfig{command: bin, args: []string{"-test.run=^TestNativeNameHelper$", "--", "--permission-mode", "bypass_permissions", "acp", "serve"}, extraEnv: []string{"CC_NAME_HELPER=" + audit}, workDir: dir, resumeSessionID: resume})
		if err != nil {
			t.Fatal(err)
		}
		if !s.SupportsSessionNaming() {
			t.Fatal("Trae naming not enabled")
		}
		id := s.CurrentSessionID()
		if err := s.SetSessionName(ctx, "容器动态挂载 · Jordan"); err != nil {
			t.Fatal(err)
		}
		if !s.Alive() || s.CurrentSessionID() != id {
			t.Fatal("metadata connection changed active ACP session")
		}
		_ = s.Close()
		raw, err := os.ReadFile(audit)
		if err != nil {
			t.Fatal(err)
		}
		var names int
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			var entry struct {
				Args   []string
				Method string
				Params map[string]any
				CWD    string
			}
			if err := json.Unmarshal([]byte(line), &entry); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(entry.Method, "prompt") || strings.HasPrefix(entry.Method, "turn/") || strings.HasPrefix(entry.Method, "thread/start") || entry.Method == "thread/resume" {
				t.Fatal("naming started model work:", line)
			}
			if entry.Method == "thread/name/set" {
				names++
				if entry.Params["threadId"] != id || entry.Params["name"] != "容器动态挂载 · Jordan" || entry.CWD != dir {
					t.Fatal(line)
				}
				args := strings.Join(entry.Args, " ")
				if !strings.HasSuffix(args, "--permission-mode bypass_permissions app-server") {
					t.Fatal("wrong metadata command:", args)
				}
			}
		}
		if names != 1 {
			t.Fatalf("name writes = %d", names)
		}
	}
}

func TestNativeNameHelper(t *testing.T) {
	path := os.Getenv("CC_NAME_HELPER")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	cwd, _ := os.Getwd()
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req struct {
			ID     any            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			panic(err)
		}
		_ = json.NewEncoder(f).Encode(map[string]any{"args": os.Args[1:], "method": req.Method, "params": req.Params, "cwd": cwd})
		if req.ID == nil {
			continue
		}
		result := map[string]any{}
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"loadSession": true}}
		case "session/new":
			result["sessionId"] = "new-thread"
		case "session/load":
			result["sessionId"] = req.Params["sessionId"]
		case "thread/name/set":
		default:
			panic(fmt.Sprintf("unexpected request %s", req.Method))
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"id": req.ID, "result": result})
	}
	os.Exit(0)
}

func TestNonTraeNativeSessionNamingUnsupported(t *testing.T) {
	if traeSessionNamer("other-agent", []string{"acp", "serve"}, nil, ".") != nil {
		t.Fatal("unrelated ACP backend opted in")
	}
	if traeSessionNamer("traecli", []string{"other-mode"}, nil, ".") != nil {
		t.Fatal("unrecognized command rewritten")
	}
}

// Opt in against a caller-selected existing thread. Reapply its existing
// name and verify through a fresh metadata connection; never open a turn.
func TestTraeNativeSessionNameIntegration(t *testing.T) {
	id := os.Getenv("CC_NAME_INTEGRATION_THREAD")
	if id == "" {
		t.Skip("set CC_NAME_INTEGRATION_THREAD for a metadata-only local check")
	}
	bin, err := exec.LookPath("traecli")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	readName := func() string {
		probe := &Agent{cmd: bin, args: []string{"app-server"}}
		tr, _, close, err := probe.probeSpawn(ctx, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer close()
		if _, err := tr.call(ctx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "cc-connect-naming-test", "version": "1.0"}}); err != nil {
			t.Fatal(err)
		}
		if err := tr.sendNotification("initialized", map[string]any{}); err != nil {
			t.Fatal(err)
		}
		raw, err := tr.call(ctx, "thread/read", map[string]any{"threadId": id, "includeTurns": false})
		if err != nil {
			t.Fatal(err)
		}
		var res struct{ Thread struct{ ID, Name string } }
		if err := json.Unmarshal(raw, &res); err != nil {
			t.Fatal(err)
		}
		if res.Thread.ID != id || res.Thread.Name == "" {
			t.Fatal("requires a named, existing thread")
		}
		return res.Thread.Name
	}
	before := readName()
	s := &acpSession{ctx: ctx, acpSessID: id, nativeNamer: traeSessionNamer(bin, []string{"--permission-mode", "bypass_permissions", "acp", "serve"}, nil, t.TempDir())}
	if err := s.SetSessionName(ctx, before); err != nil {
		t.Fatal(err)
	}
	if got := readName(); got != before {
		t.Fatalf("name changed: %q -> %q", before, got)
	}
}
