package gemini

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

type workspaceTestPlatform struct{ replies chan string }

func (p *workspaceTestPlatform) Name() string                    { return "workspace-test" }
func (p *workspaceTestPlatform) Start(core.MessageHandler) error { return nil }
func (p *workspaceTestPlatform) Stop() error                     { return nil }
func (p *workspaceTestPlatform) Reply(_ context.Context, _ any, text string) error {
	p.replies <- text
	return nil
}
func (p *workspaceTestPlatform) Send(ctx context.Context, replyCtx any, text string) error {
	return p.Reply(ctx, replyCtx, text)
}

type workspaceProcessReport struct {
	Executable  string
	Args        []string
	WorkDir     string
	ConfigEnv   string
	ProviderEnv string
	SessionEnv  string
	Prompt      string
}

// This test binary is the only subprocess used by the workspace regression.
// The fallback executable also invokes it, so a missing cmd option is safe.
func TestGeminiWorkspaceProcess(t *testing.T) {
	if os.Getenv("CC_GEMINI_WORKSPACE_HELPER") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		os.Exit(20)
	}
	executable, err := os.Executable()
	if err != nil {
		os.Exit(24)
	}
	cwd, err := os.Getwd()
	if err != nil {
		os.Exit(21)
	}
	prompt, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(22)
	}
	report, err := json.Marshal(workspaceProcessReport{
		Executable: executable, Args: args[1:], WorkDir: cwd, ConfigEnv: os.Getenv("CC_GEMINI_CONFIG"),
		ProviderEnv: os.Getenv("CC_GEMINI_PROVIDER"), SessionEnv: os.Getenv("CC_GEMINI_OLD_SESSION"), Prompt: string(prompt),
	})
	if err != nil {
		os.Exit(23)
	}
	fmt.Println(`{"type":"init","session_id":"synthetic-workspace-session"}`)
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"type": "message", "role": "assistant", "content": "WORKSPACE_REPORT:" + string(report)})
	fmt.Println(`{"type":"result","status":"success"}`)
	os.Exit(0)
}

func TestWorkspaceAgent_PreservesCommandEnvAndTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fallback fixture uses a POSIX shell")
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	bin = filepath.Join(root, "custom cli with spaces")
	if err := os.WriteFile(bin, data, 0755); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "workspace")
	binDir := filepath.Join(root, "bin")
	for _, dir := range []string{workspace, binDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	// Even the broken default-command path must only launch a local fake process.
	fallback := "#!/bin/sh\nexec '" + strings.ReplaceAll(bin, "'", "'\\''") + "' -test.run=^TestGeminiWorkspaceProcess$ -- fallback \"$@\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "gemini"), []byte(fallback), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	isolatedHome := t.TempDir()
	t.Setenv("HOME", isolatedHome)
	t.Setenv("CC_GEMINI_WORKSPACE_HELPER", "1")
	t.Logf("fake CLI executable=%q sha256=%x", bin, sha256.Sum256(data))
	t.Logf("fallback executable=%q content=%q", filepath.Join(binDir, "gemini"), fallback)
	t.Logf("child isolation: PATH=%q (only the fake fallback), HOME=%q (empty temporary home)", binDir, isolatedHome)
	for _, key := range []string{"CC_GEMINI_CONFIG", "CC_GEMINI_PROVIDER", "CC_GEMINI_OLD_SESSION", "GEMINI_API_KEY", "GOOGLE_API_KEY"} {
		t.Setenv(key, "")
	}
	command := []string{bin, "-test.run=^TestGeminiWorkspaceProcess$", "--", "prefix with spaces", `quote"and\backslash`}
	original, err := New(map[string]any{
		"cmd": command, "work_dir": root, "model": "base-model", "mode": "plan", "timeout_mins": 7,
		"env": map[string]string{"CC_GEMINI_CONFIG": "value=with spaces"},
	})
	if err != nil {
		t.Fatal(err)
	}
	base := original.(*Agent)
	base.SetProviders([]core.ProviderConfig{{Name: "synthetic", Model: "provider-model", Env: map[string]string{"CC_GEMINI_PROVIDER": "provider-value"}}})
	base.SetActiveProvider("synthetic")
	base.SetSessionEnv([]string{"CC_GEMINI_OLD_SESSION=must-not-leak"})

	// Observe the real factory call without reconstructing any options in the test.
	created := make(chan *Agent, 4)
	core.RegisterAgent("gemini", func(opts map[string]any) (core.Agent, error) {
		a, err := New(opts)
		if err == nil {
			created <- a.(*Agent)
		}
		return a, err
	})
	t.Cleanup(func() { core.RegisterAgent("gemini", New) })
	p := &workspaceTestPlatform{replies: make(chan string, 32)}
	e := core.NewEngine("gemini-workspace-regression", original, []core.Platform{p}, "", core.LangEnglish)
	e.SetMultiWorkspace(root, filepath.Join(root, "bindings.json"))
	e.SetReplyFooterEnabled(false)
	t.Cleanup(func() {
		if err := e.Stop(); err != nil {
			t.Error(err)
		}
	})
	send := func(text string) {
		e.ReceiveMessage(p, &core.Message{Platform: p.Name(), SessionKey: p.Name() + ":channel:user", ChannelKey: "channel", ChannelID: "channel", UserID: "user", Content: text, ReplyCtx: "reply"})
	}
	send("/workspace bind workspace")
	send("/status")
	var clone *Agent
	select {
	case clone = <-created:
	case <-time.After(5 * time.Second):
		t.Fatal("real workspace factory did not construct a Gemini agent")
	}
	if clone == base {
		t.Fatal("workspace reused the original agent")
	}
	if clone.cmd != bin || !reflect.DeepEqual(clone.cliExtraArgs, command[1:]) {
		t.Errorf("workspace command = %q %q, want %q", clone.cmd, clone.cliExtraArgs, command)
	}
	if clone.timeout != 7*time.Minute {
		t.Errorf("workspace timeout = %v, want 7m0s", clone.timeout)
	}
	if clone.GetWorkDir() != workspace {
		t.Errorf("workspace = %q, want %q", clone.GetWorkDir(), workspace)
	}
	send("synthetic prompt")
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case text := <-p.replies:
			_, raw, ok := strings.Cut(text, "WORKSPACE_REPORT:")
			if !ok {
				continue
			}
			var report workspaceProcessReport
			if err := json.Unmarshal([]byte(raw), &report); err != nil {
				t.Fatalf("decode process report %q: %v", raw, err)
			}
			t.Logf("synthetic child report: %s", raw)
			if report.Executable != bin {
				t.Errorf("process executable = %q, want fake CLI %q", report.Executable, bin)
			}
			wantArgs := append(append([]string{}, command[3:]...), "--output-format", "stream-json", "--approval-mode", "plan", "-m", "provider-model", "-p", "-")
			if !reflect.DeepEqual(report.Args, wantArgs) {
				t.Errorf("process args = %q, want %q", report.Args, wantArgs)
			}
			if report.ConfigEnv != "value=with spaces" {
				t.Errorf("process config env = %q, want value=with spaces", report.ConfigEnv)
			}
			if report.ProviderEnv != "provider-value" {
				t.Errorf("process provider env = %q", report.ProviderEnv)
			}
			if report.SessionEnv != "" {
				t.Errorf("old session env leaked: %q", report.SessionEnv)
			}
			if report.WorkDir != workspace {
				t.Errorf("process work_dir = %q, want %q", report.WorkDir, workspace)
			}
			if !strings.Contains(report.Prompt, "synthetic prompt") {
				t.Errorf("process prompt = %q", report.Prompt)
			}
			return
		case <-timer.C:
			t.Fatal("no fake-process report reached the platform")
		}
	}
}

func workspaceSnapshot(t *testing.T, agent core.Agent) map[string]any {
	t.Helper()
	snapshotter, ok := agent.(core.WorkspaceAgentOptionSnapshotter)
	if !ok {
		t.Fatal("Gemini must preserve constructor options when creating workspace agents")
	}
	return snapshotter.WorkspaceAgentOptions()
}

func TestWorkspaceAgentOptions_RoundTripAndIsolation(t *testing.T) {
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		timeout any
		want    time.Duration
	}{
		{"omitted", nil, 0}, {"zero", 0, 0}, {"negative", int64(-1), 0},
		{"int", 7, 7 * time.Minute}, {"int64", int64(3), 3 * time.Minute}, {"float64", float64(2), 2 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command := []string{bin, "prefix with spaces", `quote"and\backslash`, "--flag=value"}
			original, err := New(map[string]any{
				"cmd": command, "work_dir": t.TempDir(), "timeout_mins": tc.timeout,
				"env": map[string]any{"KEY": "value=with spaces", "EMPTY": ""},
			})
			if err != nil {
				t.Fatal(err)
			}
			base := original.(*Agent)
			base.SetModel("current-model")
			base.SetMode("edit")
			base.SetProviders([]core.ProviderConfig{{Name: "fixture", APIKey: "synthetic-provider-key", Model: "provider-model"}})
			base.SetActiveProvider("fixture")
			base.SetSessionEnv([]string{"SESSION_ONLY=synthetic-session"})
			opts := workspaceSnapshot(t, original)
			if _, ok := opts["work_dir"]; ok {
				t.Error("snapshot must omit work_dir")
			}
			if got := opts["model"]; got != "current-model" {
				t.Errorf("model = %v, want current-model", got)
			}
			if got := opts["mode"]; got != "auto_edit" {
				t.Errorf("mode = %v, want auto_edit", got)
			}
			if got := opts["timeout_mins"]; got != int64(tc.want/time.Minute) {
				t.Errorf("timeout_mins = %#v", got)
			}
			env, ok := opts["env"].(map[string]string)
			if !ok || !reflect.DeepEqual(env, map[string]string{"KEY": "value=with spaces", "EMPTY": ""}) {
				t.Fatalf("config env snapshot = %#v", opts["env"])
			}
			opts["work_dir"] = t.TempDir()
			recreated, err := New(opts)
			if err != nil {
				t.Fatal(err)
			}
			clone := recreated.(*Agent)
			if clone.timeout != tc.want {
				t.Errorf("recreated timeout = %v, want %v", clone.timeout, tc.want)
			}
			if clone.GetModel() != "current-model" {
				t.Errorf("provider model leaked into base model: %q", clone.GetModel())
			}
			if len(clone.providers) != 0 || len(clone.sessionEnv) != 0 {
				t.Error("provider/session state must be wired separately")
			}
			clone.SetProviders(base.ListProviders())
			clone.SetActiveProvider("fixture")
			if clone.GetModel() != "provider-model" {
				t.Errorf("active provider model = %q", clone.GetModel())
			}
			clone.SetActiveProvider("")
			if clone.GetModel() != "current-model" {
				t.Errorf("clearing provider lost base model: %q", clone.GetModel())
			}
			session, err := clone.StartSession(context.Background(), "")
			if err != nil {
				t.Fatal(err)
			}
			if session.(*geminiSession).timeout != tc.want {
				t.Errorf("session timeout = %v, want %v", session.(*geminiSession).timeout, tc.want)
			}
			if err := session.Close(); err != nil {
				t.Fatal(err)
			}
			// Neither a caller nor a recreated agent may mutate the original snapshot source.
			opts["cmd"].([]string)[1] = "mutated-prefix"
			env["KEY"] = "mutated-env"
			clone.cliExtraArgs[1] = "mutated-clone"
			clone.configEnv[0] = "KEY=mutated-clone"
			fresh := workspaceSnapshot(t, original)
			if !reflect.DeepEqual(fresh["cmd"], command) {
				t.Errorf("command snapshot aliases source: %#v", fresh["cmd"])
			}
			if !reflect.DeepEqual(fresh["env"], map[string]string{"KEY": "value=with spaces", "EMPTY": ""}) {
				t.Errorf("env snapshot aliases source: %#v", fresh["env"])
			}
		})
	}
}

func TestWorkspaceAgentOptions_Defaults(t *testing.T) {
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(map[string]any{"cmd": bin})
	if err != nil {
		t.Fatal(err)
	}
	opts := workspaceSnapshot(t, a)
	if _, ok := opts["env"]; ok {
		t.Errorf("empty config env should be omitted: %#v", opts["env"])
	}
	if opts["model"] != "" || opts["mode"] != "default" || opts["timeout_mins"] != int64(0) {
		t.Errorf("defaults changed: %#v", opts)
	}
	recreated, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if recreated.(*Agent).timeout != 0 {
		t.Error("disabled timeout was not preserved")
	}
}

func TestWorkspaceAgentOptions_ConcurrentChanges(t *testing.T) {
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(map[string]any{"cmd": []string{bin, "prefix"}, "env": map[string]string{"KEY": "value"}})
	if err != nil {
		t.Fatal(err)
	}
	snapshotter, ok := a.(core.WorkspaceAgentOptionSnapshotter)
	if !ok {
		t.Fatal("Gemini must implement WorkspaceAgentOptionSnapshotter")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 50 {
			a.(*Agent).SetModel("current-model")
			a.(*Agent).SetMode("plan")
		}
	}()
	for range 50 {
		opts := snapshotter.WorkspaceAgentOptions()
		opts["cmd"].([]string)[0] = "mutation"
		opts["env"].(map[string]string)["KEY"] = "mutation"
	}
	<-done
	if got := snapshotter.WorkspaceAgentOptions()["cmd"].([]string)[0]; got != bin {
		t.Fatalf("concurrent snapshot changed command to %q", got)
	}
}
