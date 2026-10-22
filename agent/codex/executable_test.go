package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const modernCodexLayout = "Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex"
const legacyCodexLayout = "Contents/Resources/codex"

func desktopExecutableFixture(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFindCodexExecutable_DesktopLayoutsAndBrokenPATH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("macOS executable permissions and symlinks")
	}
	for _, name := range []string{"ChatGPT.app", "Codex.app"} {
		for _, layout := range []string{modernCodexLayout, legacyCodexLayout} {
			t.Run(name+"/"+layout, func(t *testing.T) {
				root := t.TempDir()
				apps := []string{filepath.Join(root, "ChatGPT.app"), filepath.Join(root, "Codex.app")}
				want := desktopExecutableFixture(t, filepath.Join(root, name, layout))
				bin := filepath.Join(root, "bin")
				if err := os.Mkdir(bin, 0755); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", bin)
				for _, broken := range []bool{false, true} {
					if broken {
						if err := os.Symlink(filepath.Join(root, "removed-cli"), filepath.Join(bin, "codex")); err != nil {
							t.Fatal(err)
						}
					}
					got, err := findCodexExecutable("", "darwin", apps)
					if err != nil || got != want {
						t.Fatalf("broken=%v: got %q, %v; want %q", broken, got, err, want)
					}
				}
				for _, explicit := range []string{"codex", "missing-command", filepath.Join(root, "missing-cli")} {
					if _, err := findCodexExecutable(explicit, "darwin", apps); err == nil {
						t.Fatalf("invalid explicit command %q fell back", explicit)
					}
				}
				for _, goos := range []string{"linux", "windows"} {
					if _, err := findCodexExecutable("", goos, apps); err == nil {
						t.Fatalf("desktop discovery enabled on %s", goos)
					}
				}
			})
		}
	}
}

func TestFindCodexExecutable_PrecedenceAndExecutableValidation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("macOS executable permissions")
	}
	root := t.TempDir()
	apps := []string{filepath.Join(root, "ChatGPT.app"), filepath.Join(root, "Codex.app")}
	legacy := desktopExecutableFixture(t, filepath.Join(apps[0], legacyCodexLayout))
	modern := desktopExecutableFixture(t, filepath.Join(apps[0], modernCodexLayout))
	desktopExecutableFixture(t, filepath.Join(apps[1], modernCodexLayout))
	bin := filepath.Join(root, "bin")
	t.Setenv("PATH", bin)
	check := func(want string) {
		t.Helper()
		got, err := findCodexExecutable("", "darwin", apps)
		if err != nil || got != want {
			t.Fatalf("got %q, %v; want %q", got, err, want)
		}
	}
	check(modern)
	if err := os.Chmod(modern, 0644); err != nil {
		t.Fatal(err)
	}
	check(legacy)
	if err := os.Remove(modern); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(modern, 0755); err != nil {
		t.Fatal(err)
	}
	check(legacy)
	pathCLI := desktopExecutableFixture(t, filepath.Join(bin, "codex"))
	check(pathCLI)
	explicit := desktopExecutableFixture(t, filepath.Join(root, "custom path", "cli"))
	got, err := findCodexExecutable(explicit, "darwin", apps)
	if err != nil || got != explicit {
		t.Fatalf("explicit command: %q, %v", got, err)
	}
}

func TestNew_CodexCLIOverrideAndWorkspaceOptions(t *testing.T) {
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_CLI_PATH", "missing-codex-override")
	if _, err := New(map[string]any{}); err == nil {
		t.Fatal("invalid CODEX_CLI_PATH must fail")
	}
	args := []string{bin, "-test.run=^TestDesktopCLIProcess$", "--", "desktop-fixture"}
	a, err := New(map[string]any{"cmd": args})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.(*Agent).WorkspaceAgentOptions()["cmd"]; !reflect.DeepEqual(got, args) {
		t.Fatalf("workspace cmd = %#v", got)
	}
	if got := a.(core.AgentDoctorInfo).CLIBinaryName(); got != bin {
		t.Fatalf("doctor binary = %q", got)
	}
	t.Setenv("CODEX_CLI_PATH", bin)
	if _, err := New(map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(map[string]any{"cmd": "missing-configured-command"}); err == nil {
		t.Fatal("cmd must take precedence over environment")
	}
}

// The current test binary acts as a CLI with a path containing spaces and
// required prefix arguments. No real account, server, or model turn is used.
func TestDesktopCLIProcess(t *testing.T) {
	if os.Getenv("CC_DESKTOP_CLI_HELPER") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "desktop-fixture" {
		args = args[1:]
	}
	if len(args) < 2 {
		os.Exit(20)
	}
	if args[1] == "exec" {
		fmt.Println(`{"type":"thread.started","thread_id":"fixture-thread"}`)
		fmt.Println(`{"type":"item.completed","item":{"type":"agent_message","text":"desktop CLI reply"}}`)
		fmt.Println(`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`)
		os.Exit(0)
	}
	if args[1] != "app-server" {
		os.Exit(21)
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			os.Exit(22)
		}
		if req.ID == nil {
			continue
		}
		var result any = map[string]any{}
		switch req.Method {
		case "config/read":
			result = map[string]any{"config": map[string]any{"model": "fixture-model", "model_reasoning_effort": "high"}}
		case "thread/start", "thread/resume":
			result = map[string]any{"thread": map[string]any{"id": "fixture-thread"}, "model": "fixture-model"}
		case "skills/list":
			cwd, _ := os.Getwd()
			result = map[string]any{"data": []any{map[string]any{"cwd": cwd, "skills": []any{}}}}
		}
		if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"id": req.ID, "result": result}); err != nil {
			os.Exit(23)
		}
	}
	os.Exit(0)
}

func TestStartSession_UsesConfiguredCLIForBothBackendsAndMetadata(t *testing.T) {
	oldTimeout := codexRuntimeConfigTimeout
	codexRuntimeConfigTimeout = 5 * time.Second
	t.Cleanup(func() { codexRuntimeConfigTimeout = oldTimeout })
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(root, "desktop app", filepath.Base(bin))
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(copyPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copyPath, data, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	t.Setenv("CODEX_CLI_PATH", "missing-override")
	for _, backend := range []string{"exec", "app_server"} {
		t.Run(backend, func(t *testing.T) {
			a, err := New(map[string]any{
				"cmd":     []string{copyPath, "-test.run=^TestDesktopCLIProcess$", "--", "desktop-fixture"},
				"backend": backend, "work_dir": root, "codex_home": filepath.Join(root, "isolated-home"),
				"env": map[string]string{"CC_DESKTOP_CLI_HELPER": "1"},
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			session, err := a.StartSession(ctx, "")
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := session.Close(); err != nil {
					t.Errorf("close session: %v", err)
				}
			}()
			if cs, ok := session.(*codexSession); ok {
				if got := cs.GetModel(); got != "fixture-model" {
					t.Fatalf("runtime metadata model = %q", got)
				}
				if err := cs.Send("hello", "fixture", nil, nil); err != nil {
					t.Fatal(err)
				}
				var text string
				for {
					select {
					case event := <-cs.Events():
						text += event.Content
						if event.Type == core.EventError {
							t.Fatalf("turn error: %+v", event)
						}
						if event.Type == core.EventResult {
							if !strings.Contains(text, "desktop CLI reply") {
								t.Fatalf("missing reply: %q", text)
							}
							goto complete
						}
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
			}
		complete:
			if _, err := a.(*Agent).ListSkills(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}
