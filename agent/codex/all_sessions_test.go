package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func writeAllSessionsRollout(t *testing.T, home, id, cwd string, source any) string {
	t.Helper()
	dir := filepath.Join(home, "sessions", "2026", "10", "26")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta, err := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{
		"id": id, "cwd": cwd, "source": source,
	}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-"+id+".jsonl")
	if err := os.WriteFile(path, append(meta, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAgentListSessions_AllDirectoriesIsOptInAndProfileScoped(t *testing.T) {
	home, otherHome := t.TempDir(), t.TempDir()
	dirA, dirB := t.TempDir(), t.TempDir()
	writeAllSessionsRollout(t, home, "session-a", dirA, "vscode")
	writeAllSessionsRollout(t, home, "session-b", dirB, "cli")
	writeAllSessionsRollout(t, home, "helper", dirB, map[string]any{"subagent": map[string]any{}})
	archived := writeAllSessionsRollout(t, home, "archived", dirB, "cli")
	if err := os.Mkdir(filepath.Join(home, "archived_sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(archived, filepath.Join(home, "archived_sessions", filepath.Base(archived))); err != nil {
		t.Fatal(err)
	}
	writeAllSessionsRollout(t, otherHome, "different-profile", dirA, "cli")
	t.Setenv("CODEX_HOME", otherHome)
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		option any
		want   []string
	}{
		{"omitted", nil, []string{"session-a"}},
		{"false", false, []string{"session-a"}},
		{"true", true, []string{"session-a", "session-b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := map[string]any{"cmd": bin, "work_dir": dirA, "codex_home": home}
			if tc.option != nil {
				opts["list_all_sessions"] = tc.option
			}
			agent, err := New(opts)
			if err != nil {
				t.Fatal(err)
			}
			assertIDs := func(a core.Agent) {
				t.Helper()
				sessions, err := a.ListSessions(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				var ids []string
				for _, session := range sessions {
					ids = append(ids, session.ID)
				}
				sort.Strings(ids)
				if !reflect.DeepEqual(ids, tc.want) {
					t.Fatalf("session IDs = %v, want %v", ids, tc.want)
				}
			}
			assertIDs(agent)
			clonedOpts := agent.(*Agent).WorkspaceAgentOptions()
			clonedOpts["work_dir"] = dirA
			clone, err := New(clonedOpts)
			if err != nil {
				t.Fatal(err)
			}
			assertIDs(clone)
		})
	}
}

// This subprocess fixture observes the actual cwd and attachment paths at the
// Codex boundary. It never talks to a model or uses the real Codex profile.
func TestAllSessionsCLIProcess(t *testing.T) {
	if os.Getenv("CC_ALL_SESSIONS_HELPER") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "all-sessions-fixture" {
		args = args[1:]
	}
	if len(args) < 2 {
		os.Exit(20)
	}
	cwd, _ := os.Getwd()
	want := os.Getenv("CC_ALL_SESSIONS_EXPECT_CWD")
	if home := os.Getenv("CC_ALL_SESSIONS_EXPECT_HOME"); home != "" && os.Getenv("CODEX_HOME") != home {
		os.Exit(28)
	}
	if cwd != want {
		fmt.Fprintf(os.Stderr, "process cwd = %q, want %q\n", cwd, want)
		os.Exit(21)
	}
	if args[1] == "exec" {
		prompt, _ := io.ReadAll(os.Stdin)
		if !strings.Contains(string(prompt), filepath.Join(want, ".cc-connect")) {
			os.Exit(22)
		}
		for i, arg := range args {
			if arg == "--image" && i+1 < len(args) && !strings.HasPrefix(args[i+1], want+string(filepath.Separator)) {
				os.Exit(23)
			}
		}
		fmt.Println(`{"type":"thread.started","thread_id":"session-b"}`)
		fmt.Println(`{"type":"item.completed","item":{"type":"agent_message","text":"resumed"}}`)
		fmt.Println(`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`)
		os.Exit(0)
	}
	if args[1] != "app-server" {
		os.Exit(24)
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request struct {
			ID     any                        `json:"id"`
			Method string                     `json:"method"`
			Params map[string]json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(25)
		}
		if request.ID == nil {
			continue
		}
		var result any = map[string]any{}
		switch request.Method {
		case "thread/start", "thread/resume":
			id := "new-session"
			if request.Method == "thread/resume" {
				_ = json.Unmarshal(request.Params["threadId"], &id)
			}
			result = map[string]any{"cwd": cwd, "thread": map[string]any{"id": id}}
		case "turn/start":
			var inputs []struct {
				Type string `json:"type"`
				Text string `json:"text"`
				Path string `json:"path"`
			}
			_ = json.Unmarshal(request.Params["input"], &inputs)
			for _, input := range inputs {
				if input.Type == "text" && !strings.Contains(input.Text, filepath.Join(want, ".cc-connect")) {
					os.Exit(26)
				}
				if input.Type == "localImage" && !strings.HasPrefix(input.Path, want+string(filepath.Separator)) {
					os.Exit(27)
				}
			}
			result = map[string]any{"turn": map[string]any{"id": "turn-1"}}
			_ = encoder.Encode(map[string]any{"method": "turn/started", "params": map[string]any{
				"threadId": "session-b", "turn": map[string]any{"id": "turn-1", "status": "inProgress"},
			}})
		}
		_ = encoder.Encode(map[string]any{"id": request.ID, "result": result})
		if request.Method == "turn/start" {
			_ = encoder.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{
				"threadId": "session-b", "turn": map[string]any{"id": "turn-1", "status": "completed"},
			}})
		}
	}
	os.Exit(0)
}

func TestStartSession_AllDirectoriesRestoresCwdWithoutChangingOtherTopics(t *testing.T) {
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, backend := range []string{"exec", "app_server"} {
		t.Run(backend, func(t *testing.T) {
			dirA, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			dirB, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			home := t.TempDir()
			writeAllSessionsRollout(t, home, "session-a", dirA, "cli")
			writeAllSessionsRollout(t, home, "session-b", dirB, "vscode")
			agent, err := New(map[string]any{
				"cmd":      []string{bin, "-test.run=^TestAllSessionsCLIProcess$", "--", "all-sessions-fixture"},
				"work_dir": dirA, "codex_home": home, "backend": backend, "list_all_sessions": true,
				"env": map[string]string{"CC_ALL_SESSIONS_HELPER": "1", "CC_ALL_SESSIONS_EXPECT_CWD": dirB},
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			resumed, err := agent.StartSession(ctx, "session-b")
			if err != nil {
				t.Fatal(err)
			}
			defer resumed.Close()
			if got := resumed.(interface{ GetWorkDir() string }).GetWorkDir(); got != dirB {
				t.Fatalf("resumed cwd = %q, want %q", got, dirB)
			}
			if err := resumed.Send("read attachments", "cross-directory", []core.ImageAttachment{
				{MimeType: "image/png", Data: []byte("image-data")},
			}, []core.FileAttachment{{FileName: "input.txt", Data: []byte("file-data")}}); err != nil {
				t.Fatal(err)
			}
			waitForDoneResult(t, resumed.Events())
			if got := agent.(*Agent).GetWorkDir(); got != dirA {
				t.Fatalf("other topics' default changed to %q", got)
			}
			if _, err := os.Stat(filepath.Join(dirA, ".cc-connect")); !os.IsNotExist(err) {
				t.Fatalf("attachments touched the default cwd: %v", err)
			}
			agent.(*Agent).SetSessionEnv([]string{"CC_ALL_SESSIONS_EXPECT_CWD=" + dirA})
			for _, id := range []string{"session-a", ""} {
				other, err := agent.StartSession(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				if got := other.(interface{ GetWorkDir() string }).GetWorkDir(); got != dirA {
					t.Fatalf("other/new session cwd = %q, want %q", got, dirA)
				}
				_ = other.Close()
			}
		})
	}
}

func TestStartSession_AllDirectoriesRejectsMissingSavedDirectory(t *testing.T) {
	home, configured := t.TempDir(), t.TempDir()
	writeAllSessionsRollout(t, home, "missing-dir", filepath.Join(t.TempDir(), "deleted"), "cli")
	for _, backend := range []string{"exec", "app_server"} {
		a := &Agent{workDir: configured, codexHome: home, backend: backend, listAllSessions: true, activeIdx: -1}
		if session, err := a.StartSession(context.Background(), "missing-dir"); err == nil || session != nil {
			t.Fatalf("%s resumed without its saved directory: session=%v error=%v", backend, session, err)
		}
		if a.GetWorkDir() != configured {
			t.Fatal("default directory changed")
		}
	}
}

func TestStartSession_AllDirectoriesPinsRelativeCodexHome(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	home := filepath.Join(root, "relative-profile")
	dirA, dirB := t.TempDir(), t.TempDir()
	writeAllSessionsRollout(t, home, "session-b", dirB, "cli")
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprintf("explicit=%v", explicit), func(t *testing.T) {
			t.Setenv("CODEX_HOME", "relative-profile")
			opts := map[string]any{"cmd": bin, "work_dir": dirA, "list_all_sessions": true}
			if explicit {
				opts["codex_home"] = "relative-profile"
				t.Setenv("CODEX_HOME", filepath.Join(root, "wrong-profile"))
			}
			agent, err := New(opts)
			if err != nil {
				t.Fatal(err)
			}
			session, err := agent.StartSession(context.Background(), "session-b")
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			cs := session.(*codexSession)
			if got := getenvFromList(cs.extraEnv, "CODEX_HOME"); got != home {
				t.Fatalf("subprocess Codex home = %q, want %q", got, home)
			}
			if cs.GetWorkDir() != dirB {
				t.Fatalf("session cwd = %q, want %q", cs.GetWorkDir(), dirB)
			}
		})
	}
}

func TestAllDirectories_PreservesCodexHomeEnvironmentOverrides(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	homes := map[string]string{}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"process", "project", "provider", "session", "explicit", "default"} {
		home := filepath.Join(root, source)
		if source == "default" {
			home = filepath.Join(root, ".codex")
		}
		homes[source] = home
		path := writeAllSessionsRollout(t, home, "id-"+source, dir, "cli")
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString(`{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"profile-specific history"}]}}` + "\n")
		_ = f.Close()
	}
	t.Setenv("CODEX_HOME", homes["process"])
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, backend := range []string{"exec", "app_server"} {
		for _, tc := range []struct {
			name, project, provider, session, explicit, want string
			clear                                            bool
		}{
			{name: "project", project: "project", want: "project"},
			{name: "provider", project: "project", provider: "provider", want: "provider"},
			{name: "session", project: "project", provider: "provider", session: "session", want: "session"},
			{name: "explicit", project: "project", session: "session", explicit: "explicit", want: "explicit"},
			{name: "cleared", project: "project", clear: true, want: "default"},
		} {
			t.Run(backend+"/"+tc.name, func(t *testing.T) {
				env := map[string]string{"CC_ALL_SESSIONS_HELPER": "1", "CC_ALL_SESSIONS_EXPECT_CWD": dir, "CC_ALL_SESSIONS_EXPECT_HOME": homes[tc.want]}
				if tc.project != "" {
					env["CODEX_HOME"] = homes[tc.project]
				}
				opts := map[string]any{"cmd": []string{bin, "-test.run=^TestAllSessionsCLIProcess$", "--", "all-sessions-fixture"}, "work_dir": dir, "backend": backend, "list_all_sessions": true, "env": env}
				if tc.explicit != "" {
					opts["codex_home"] = homes[tc.explicit]
				}
				a, err := New(opts)
				if err != nil {
					t.Fatal(err)
				}
				agent := a.(*Agent)
				if tc.provider != "" {
					agent.SetProviders([]core.ProviderConfig{{Name: "test-provider", Env: map[string]string{"CODEX_HOME": homes[tc.provider]}}})
					if !agent.SetActiveProvider("test-provider") {
						t.Fatal("provider not found")
					}
				}
				if tc.session != "" {
					agent.SetSessionEnv([]string{"CODEX_HOME=" + homes[tc.session]})
				}
				if tc.clear {
					agent.SetSessionEnv([]string{"CODEX_HOME="})
				}
				sessions, err := agent.ListSessions(context.Background())
				id := "id-" + tc.want
				if err != nil || len(sessions) != 1 || sessions[0].ID != id {
					t.Fatalf("sessions=%v err=%v, want %s", sessions, err, id)
				}
				history, err := agent.GetSessionHistory(context.Background(), id, 1)
				if err != nil || len(history) != 1 {
					t.Fatalf("wrong profile history: %v, %v", history, err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				for _, resumeID := range []string{"", id} {
					session, err := agent.StartSession(ctx, resumeID)
					if err != nil {
						t.Fatal(err)
					}
					if cs, ok := session.(*codexSession); ok && getenvFromList(cs.extraEnv, "CODEX_HOME") != homes[tc.want] {
						t.Fatal("exec selected another Codex home")
					}
					_ = session.Close()
				}
			})
		}
	}
}
