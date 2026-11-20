package main

import (
	"bytes"
	"context"
	"flag"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

type stubMainAgent struct {
	workDir string
}

func (a *stubMainAgent) Name() string { return "stub-main" }

func (a *stubMainAgent) StartSession(_ context.Context, _ string) (core.AgentSession, error) {
	return &stubMainAgentSession{}, nil
}

func (a *stubMainAgent) ListSessions(_ context.Context) ([]core.AgentSessionInfo, error) {
	return nil, nil
}

func (a *stubMainAgent) Stop() error { return nil }

func (a *stubMainAgent) SetWorkDir(dir string) {
	a.workDir = dir
}

func (a *stubMainAgent) GetWorkDir() string {
	return a.workDir
}

type stubMainAgentSession struct{}

func (s *stubMainAgentSession) Send(string, string, []core.ImageAttachment, []core.FileAttachment) error {
	return nil
}
func (s *stubMainAgentSession) RespondPermission(string, core.PermissionResult) error { return nil }
func (s *stubMainAgentSession) Events() <-chan core.Event                             { return nil }
func (s *stubMainAgentSession) Close() error                                          { return nil }
func (s *stubMainAgentSession) CurrentSessionID() string                              { return "" }
func (s *stubMainAgentSession) Alive() bool                                           { return true }

type versionProbeTransport func(*http.Request) (*http.Response, error)

func (f versionProbeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Run the real entry point in a child so os.Exit and logging cannot affect
// other tests. Any accidental update request stays inside this fake transport.
func TestVersionProbeHelperProcess(t *testing.T) {
	if os.Getenv("CC_CONNECT_VERSION_PROBE_TEST") != "1" {
		return
	}
	separator := 0
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator == 0 {
		t.Fatal("missing helper argument separator")
	}
	os.Args = append([]string{"cc-connect"}, os.Args[separator+1:]...)
	version, commit, buildTime = "v0.0.0-test", "test-commit", "test-build"
	requests := make(chan struct{}, 1)
	http.DefaultTransport = versionProbeTransport(func(*http.Request) (*http.Response, error) {
		select {
		case requests <- struct{}{}:
		default:
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"tag_name":"v0.0.0-test"}`))}, nil
	})
	main()
	select {
	case <-requests:
		t.Fatal("version query started a background update request")
	case <-time.After(50 * time.Millisecond):
	}
	os.Exit(0)
}

func runVersionProbe(t *testing.T, dir, logFile string, args ...string) (string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], append([]string{"-test.run=^TestVersionProbeHelperProcess$", "--"}, args...)...)
	cmd.Dir = dir
	cmd.Env = core.MergeEnv(os.Environ(), []string{
		"CC_CONNECT_VERSION_PROBE_TEST=1", "CC_LOG_FILE=" + logFile,
		"CC_LOG_MAX_SIZE=1", "CC_LOG_MAX_BACKUPS=1", "GORACE=atexit_sleep_ms=0",
	})
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func TestVersionProbeHasNoDaemonSideEffects(t *testing.T) {
	for _, logState := range []string{"new", "existing", "invalid_parent"} {
		for _, variant := range []string{"long", "short", "explicit_true", "config", "invalid_log_size"} {
			t.Run(logState+"/"+variant, func(t *testing.T) {
				dir := t.TempDir()
				logFile := filepath.Join(dir, "new-directory", "daemon.log")
				switch logState {
				case "existing":
					logFile = filepath.Join(dir, "daemon.log")
					for _, file := range []string{logFile, logFile + ".1"} {
						if err := os.WriteFile(file, []byte("existing log contents\n"), 0600); err != nil {
							t.Fatal(err)
						}
					}
				case "invalid_parent":
					parent := filepath.Join(dir, "regular-file")
					if err := os.WriteFile(parent, []byte("not a directory"), 0600); err != nil {
						t.Fatal(err)
					}
					logFile = filepath.Join(parent, "daemon.log")
				}
				args := []string{"--version"}
				switch variant {
				case "short":
					args = []string{"-version"}
				case "explicit_true":
					args = []string{"--version=true"}
				case "config":
					args = []string{"--config", filepath.Join(dir, "missing.toml"), "--version"}
				case "invalid_log_size":
					args = []string{"--log-max-size", "invalid", "--version"}
				}
				stdout, stderr, err := runVersionProbe(t, dir, logFile, args...)
				if err != nil {
					t.Fatalf("version query: %v; stderr=%s", err, stderr)
				}
				if stdout != "cc-connect v0.0.0-test\ncommit:  test-commit\nbuilt:   test-build\n" || stderr != "" {
					t.Fatalf("unexpected output: stdout=%q stderr=%q", stdout, stderr)
				}
				switch logState {
				case "existing":
					for _, file := range []string{logFile, logFile + ".1"} {
						data, err := os.ReadFile(file)
						if err != nil || string(data) != "existing log contents\n" {
							t.Fatalf("version query changed log %s: %q, %v", file, data, err)
						}
					}
				case "new":
					if _, err := os.Stat(filepath.Dir(logFile)); !os.IsNotExist(err) {
						t.Fatalf("version query created the log directory: %v", err)
					}
				}
			})
		}
	}
}

func TestVersionFalseRetainsNormalStartup(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, "regular-file")
	if err := os.WriteFile(parent, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := runVersionProbe(t, dir, filepath.Join(parent, "daemon.log"), "--version=true", "--version=false")
	if err == nil || !strings.Contains(stderr, "failed to open log file") || stdout != "" {
		t.Fatalf("--version=false bypassed normal startup: err=%v stdout=%q stderr=%q", err, stdout, stderr)
	}
}

func TestParseRootCLIOptionsVersionSemantics(t *testing.T) {
	for _, args := range [][]string{
		{"--version=false"},
		{"--version=true", "--version=false"},
		{"--config", "--version"},
		{"send", "--version"},
	} {
		opts, err := parseRootCLIOptions(args)
		if err != nil || opts.showVersion {
			t.Fatalf("args %q: showVersion=%v, error=%v", args, opts.showVersion, err)
		}
	}
}

func TestProjectStatePath(t *testing.T) {
	dataDir := t.TempDir()
	got := projectStatePath(dataDir, "my/project:one")
	want := filepath.Join(dataDir, "projects", "my_project_one.state.json")
	if got != want {
		t.Fatalf("projectStatePath() = %q, want %q", got, want)
	}
}

func TestResolveResetOnIdle(t *testing.T) {
	intPtr := func(v int) *int { return &v }

	cases := []struct {
		name          string
		configured    *int
		wantDuration  time.Duration
		wantDefaulted bool
	}{
		{
			name:          "unset applies default and reports defaulted",
			configured:    nil,
			wantDuration:  time.Duration(defaultResetOnIdleMins) * time.Minute,
			wantDefaulted: true,
		},
		{
			name:          "explicit zero opts out and is not defaulted",
			configured:    intPtr(0),
			wantDuration:  0,
			wantDefaulted: false,
		},
		{
			name:          "explicit positive value is honored",
			configured:    intPtr(45),
			wantDuration:  45 * time.Minute,
			wantDefaulted: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotDuration, gotDefaulted := resolveResetOnIdle(tc.configured)
			if gotDuration != tc.wantDuration {
				t.Errorf("duration = %v, want %v", gotDuration, tc.wantDuration)
			}
			if gotDefaulted != tc.wantDefaulted {
				t.Errorf("defaulted = %v, want %v", gotDefaulted, tc.wantDefaulted)
			}
		})
	}
}

func TestApplyProjectStateOverride(t *testing.T) {
	baseDir := t.TempDir()
	overrideDir := filepath.Join(t.TempDir(), "override")
	if err := os.Mkdir(overrideDir, 0o755); err != nil {
		t.Fatalf("mkdir override dir: %v", err)
	}

	store := core.NewProjectStateStore(filepath.Join(t.TempDir(), "projects", "demo.state.json"))
	store.SetWorkDirOverride(overrideDir)

	agent := &stubMainAgent{workDir: baseDir}
	got := applyProjectStateOverride("demo", agent, baseDir, store)

	if got != overrideDir {
		t.Fatalf("applyProjectStateOverride() = %q, want %q", got, overrideDir)
	}
	if agent.workDir != overrideDir {
		t.Fatalf("agent workDir = %q, want %q", agent.workDir, overrideDir)
	}
}

// In multi-workspace mode the working directory is decided per topic by the
// binding table, and each workspace gets its own agent. A project-wide
// work_dir_override left over from single-workspace mode must not be pushed
// onto the global agent: it would shadow every binding, so /status reports the
// same directory in all topics.
func TestApplyProjectStateOverride_MultiWorkspaceKeepsConfiguredWorkDir(t *testing.T) {
	baseDir := t.TempDir()
	overrideDir := filepath.Join(t.TempDir(), "override")
	if err := os.Mkdir(overrideDir, 0o755); err != nil {
		t.Fatalf("mkdir override dir: %v", err)
	}

	store := core.NewProjectStateStore(filepath.Join(t.TempDir(), "projects", "demo.state.json"))
	store.SetWorkDirOverride(overrideDir)

	agent := &stubMainAgent{workDir: baseDir}
	got := applyProjectStateOverrideForMode("demo", agent, baseDir, store, true)

	if got != baseDir {
		t.Fatalf("effective workDir = %q, want the configured %q", got, baseDir)
	}
	if agent.workDir != baseDir {
		t.Fatalf("global agent workDir = %q, want %q; a project-wide override must not shadow the binding table",
			agent.workDir, baseDir)
	}
}

type stubProviderRefreshAgent struct {
	stubMainAgent
	providers  []core.ProviderConfig
	activeName string
	calls      []string
	activateOK bool
}

func (a *stubProviderRefreshAgent) SetProviders(providers []core.ProviderConfig) {
	a.providers = append([]core.ProviderConfig(nil), providers...)
	a.calls = append(a.calls, "set_providers")
}

func (a *stubProviderRefreshAgent) SetActiveProvider(name string) bool {
	if !a.activateOK {
		a.calls = append(a.calls, "set_active_provider_failed")
		return false
	}
	a.activeName = name
	a.calls = append(a.calls, "set_active_provider")
	return true
}

func (a *stubProviderRefreshAgent) GetActiveProvider() *core.ProviderConfig {
	for i := range a.providers {
		if a.providers[i].Name == a.activeName {
			return &a.providers[i]
		}
	}
	return nil
}

func (a *stubProviderRefreshAgent) ListProviders() []core.ProviderConfig {
	providers := make([]core.ProviderConfig, len(a.providers))
	copy(providers, a.providers)
	return providers
}

func (a *stubProviderRefreshAgent) StartInitialModelRefresh() {
	a.calls = append(a.calls, "start_initial_model_refresh")
}

func TestBuildAgentOptionsInjectsProjectScope(t *testing.T) {
	proj := config.ProjectConfig{
		Name: "demo-project",
		Agent: config.AgentConfig{
			Options: map[string]any{
				"work_dir": "/tmp/work",
				"model":    "gpt-test",
			},
		},
	}

	got := buildAgentOptions("/tmp/data", proj)

	if got["cc_data_dir"] != "/tmp/data" {
		t.Fatalf("cc_data_dir = %v, want %q", got["cc_data_dir"], "/tmp/data")
	}
	if got["cc_project"] != "demo-project" {
		t.Fatalf("cc_project = %v, want %q", got["cc_project"], "demo-project")
	}
	if got["work_dir"] != "/tmp/work" || got["model"] != "gpt-test" {
		t.Fatalf("buildAgentOptions() lost existing options: %v", got)
	}
	if _, exists := proj.Agent.Options["cc_data_dir"]; exists {
		t.Fatalf("project agent options mutated: %v", proj.Agent.Options)
	}
}

func TestWireAgentProvidersStartsRefreshAfterProviderWiring(t *testing.T) {
	agent := &stubProviderRefreshAgent{activateOK: true}
	proj := config.ProjectConfig{
		Agent: config.AgentConfig{
			Options: map[string]any{"provider": "provider-b"},
			Providers: []config.ProviderConfig{
				{Name: "provider-a", APIKey: "key-a"},
				{Name: "provider-b", APIKey: "key-b", Model: "model-b"},
			},
		},
	}

	result := wireAgentProviders(agent, proj.Agent)
	startInitialRefreshIfReady(agent, result)

	wantCalls := []string{"set_providers", "set_active_provider", "start_initial_model_refresh"}
	if !reflect.DeepEqual(agent.calls, wantCalls) {
		t.Fatalf("call order = %v, want %v", agent.calls, wantCalls)
	}
	if len(agent.providers) != 2 {
		t.Fatalf("provider count = %d, want 2", len(agent.providers))
	}
	if agent.activeName != "provider-b" {
		t.Fatalf("active provider = %q, want %q", agent.activeName, "provider-b")
	}
}

func TestWireAgentProviders_SkipsRefreshWhenExplicitProviderActivationFails(t *testing.T) {
	agent := &stubProviderRefreshAgent{}
	agent.activateOK = false
	agent.workDir = "/tmp/original"
	proj := config.ProjectConfig{
		Agent: config.AgentConfig{
			Options:   map[string]any{"provider": "missing-provider"},
			Providers: []config.ProviderConfig{{Name: "provider-a", APIKey: "key-a"}},
		},
	}

	result := wireAgentProviders(agent, proj.Agent)

	if result.canStartInitialRefresh {
		t.Fatal("canStartInitialRefresh = true, want false")
	}
	if !result.explicitProviderRequested {
		t.Fatal("explicitProviderRequested = false, want true")
	}
	if result.activeProviderApplied {
		t.Fatal("activeProviderApplied = true, want false")
	}
	wantCalls := []string{"set_providers", "set_active_provider_failed"}
	if !reflect.DeepEqual(agent.calls, wantCalls) {
		t.Fatalf("call order = %v, want %v", agent.calls, wantCalls)
	}
}

func TestWireAgentProviders_AllowsRefreshWithoutProviders(t *testing.T) {
	agent := &stubProviderRefreshAgent{stubMainAgent: stubMainAgent{workDir: "/tmp/original"}}
	proj := config.ProjectConfig{Agent: config.AgentConfig{Options: map[string]any{}}}

	result := wireAgentProviders(agent, proj.Agent)

	if !result.canStartInitialRefresh {
		t.Fatal("canStartInitialRefresh = false, want true")
	}
	if result.explicitProviderRequested {
		t.Fatal("explicitProviderRequested = true, want false")
	}
	if result.activeProviderApplied {
		t.Fatal("activeProviderApplied = true, want false")
	}
	if len(agent.calls) != 0 {
		t.Fatalf("calls = %v, want no provider wiring calls", agent.calls)
	}
}

func TestStartInitialRefresh_AfterProjectStateOverride(t *testing.T) {
	agent := &stubProviderRefreshAgent{activateOK: true, stubMainAgent: stubMainAgent{workDir: "/tmp/original"}}
	overrideDir := filepath.Join(t.TempDir(), "override")
	if err := os.Mkdir(overrideDir, 0o755); err != nil {
		t.Fatalf("mkdir override dir: %v", err)
	}
	store := core.NewProjectStateStore(filepath.Join(t.TempDir(), "projects", "demo.state.json"))
	store.SetWorkDirOverride(overrideDir)
	proj := config.ProjectConfig{
		Name: "demo",
		Agent: config.AgentConfig{
			Options:   map[string]any{"provider": "provider-b", "work_dir": "/tmp/original"},
			Providers: []config.ProviderConfig{{Name: "provider-a"}, {Name: "provider-b"}},
		},
	}

	result := wireAgentProviders(agent, proj.Agent)
	applyProjectStateOverride(proj.Name, agent, "/tmp/original", store)
	startInitialRefreshIfReady(agent, result)

	wantCalls := []string{"set_providers", "set_active_provider", "start_initial_model_refresh"}
	if !reflect.DeepEqual(agent.calls, wantCalls) {
		t.Fatalf("call order = %v, want %v", agent.calls, wantCalls)
	}
	if agent.workDir != overrideDir {
		t.Fatalf("agent workDir at refresh = %q, want %q", agent.workDir, overrideDir)
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	defer func() {
		os.Stderr = old
	}()

	// Drain concurrently. A Windows anonymous pipe buffers only 4KB, and
	// printUsage emits its whole help text in a single Fprintf, so reading
	// after fn returns deadlocks as soon as that output exceeds the buffer.
	var buf bytes.Buffer
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(&buf, r)
		done <- err
	}()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("copy stderr: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close reader: %v", err)
	}
	return buf.String()
}

func TestPrintUsage_ListsCronExecCommand(t *testing.T) {
	out := captureStderr(t, printUsage)

	if !strings.Contains(out, "Manage scheduled tasks") {
		t.Fatalf("printUsage() output missing cron section:\n%s", out)
	}
	if !strings.Contains(out, "exec             Trigger a scheduled task immediately") {
		t.Fatalf("printUsage() output missing cron exec command:\n%s", out)
	}
}

func TestCanonicalCronSubcommand_ManualTriggerAliases(t *testing.T) {
	for _, sub := range []string{"exec", "run", "trigger"} {
		if got := canonicalCronSubcommand(sub); got != "exec" {
			t.Fatalf("canonicalCronSubcommand(%q) = %q, want exec", sub, got)
		}
	}
}

func TestValidateNoExtraTopLevelArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name: "no extra args",
			args: nil,
		},
		{
			name:    "unknown command",
			args:    []string{"bind", "--help"},
			wantErr: "unknown top-level command: bind",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateNoExtraTopLevelArgs(tt.args)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateNoExtraTopLevelArgs(%v) error = %v, want nil", tt.args, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateNoExtraTopLevelArgs(%v) error = nil, want %q", tt.args, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateNoExtraTopLevelArgs(%v) error = %q, want substring %q", tt.args, err.Error(), tt.wantErr)
			}
		})
	}
}

func TestParseRootCLIOptionsGlobalFlagsBeforeSubcommand(t *testing.T) {
	opts, err := parseRootCLIOptions([]string{"--config", "/tmp/test-config.toml", "--log-max-size", "12MB", "--log-max-backups", "7", "sessions", "list"})
	if err != nil {
		t.Fatalf("parseRootCLIOptions() error = %v", err)
	}
	if opts.configPath != "/tmp/test-config.toml" {
		t.Fatalf("configPath = %q, want %q", opts.configPath, "/tmp/test-config.toml")
	}
	if opts.logMaxSize != "12MB" {
		t.Fatalf("logMaxSize = %q, want %q", opts.logMaxSize, "12MB")
	}
	if opts.logMaxBackups != 7 {
		t.Fatalf("logMaxBackups = %d, want 7", opts.logMaxBackups)
	}
	if opts.showVersion {
		t.Fatal("showVersion = true, want false")
	}
	wantArgs := []string{"sessions", "list"}
	if !reflect.DeepEqual(opts.args, wantArgs) {
		t.Fatalf("args = %v, want %v", opts.args, wantArgs)
	}
}

func TestTopLevelCommandHandlersIncludeTimerAliases(t *testing.T) {
	for _, command := range []string{"timer", "at"} {
		if topLevelCommandHandlers[command] == nil {
			t.Fatalf("topLevelCommandHandlers[%q] is nil", command)
		}
	}
}

func TestParseRootCLIOptionsPreservesSubcommandHelp(t *testing.T) {
	opts, err := parseRootCLIOptions([]string{"--config", "/tmp/test-config.toml", "send", "--help"})
	if err != nil {
		t.Fatalf("parseRootCLIOptions() error = %v", err)
	}
	wantArgs := []string{"send", "--help"}
	if !reflect.DeepEqual(opts.args, wantArgs) {
		t.Fatalf("args = %v, want %v", opts.args, wantArgs)
	}
}

func TestParseRootCLIOptionsHelp(t *testing.T) {
	_, err := parseRootCLIOptions([]string{"--help"})
	if err == nil {
		t.Fatal("parseRootCLIOptions(--help) error = nil, want flag.ErrHelp")
	}
	if !strings.Contains(err.Error(), flag.ErrHelp.Error()) {
		t.Fatalf("parseRootCLIOptions(--help) error = %q, want %q", err.Error(), flag.ErrHelp.Error())
	}
}

func TestRunTopLevelCommandUnknown(t *testing.T) {
	if runTopLevelCommand([]string{"bind", "--help"}) {
		t.Fatal("runTopLevelCommand() handled unknown command")
	}
}
