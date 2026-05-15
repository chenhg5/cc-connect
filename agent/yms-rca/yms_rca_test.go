package ymsagent

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestNormalizeMode(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "default"},
		{"default", "default"},
		{"unknown", "default"},
		{"yolo", "yolo"},
		{"YOLO", "yolo"},
		{"auto-approve", "yolo"},
		{"bypass", "bypassPermissions"},
		{"bypassPermissions", "bypassPermissions"},
		{"dontAsk", "dontAsk"},
		{"dont-ask", "dontAsk"},
	}
	for _, c := range cases {
		if got := normalizeMode(c.in); got != c.want {
			t.Errorf("normalizeMode(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBuildArgs(t *testing.T) {
	cases := []struct {
		name       string
		agent      *Agent
		resumeFile string
		want       []string
	}{
		{
			name:  "minimal",
			agent: &Agent{},
			want:  []string{"rpc", "--no-color"},
		},
		{
			name:  "model only",
			agent: &Agent{model: "yonyou/foo"},
			want:  []string{"rpc", "--no-color", "--model", "yonyou/foo"},
		},
		{
			name:  "provider + model",
			agent: &Agent{provider: "yonyou", model: "yonyou/foo"},
			want:  []string{"rpc", "--no-color", "--provider", "yonyou", "--model", "yonyou/foo"},
		},
		{
			name:  "thinking + session_dir + offline",
			agent: &Agent{thinking: "medium", sessionDir: "/sd", offline: true},
			want:  []string{"rpc", "--no-color", "--thinking", "medium", "--session-dir", "/sd", "--offline"},
		},
		{
			name:       "resumeFile passed",
			agent:      &Agent{},
			resumeFile: "/tmp/x.jsonl",
			want:       []string{"rpc", "--no-color", "--session-file", "/tmp/x.jsonl"},
		},
		{
			name:       "all options",
			agent:      &Agent{provider: "p", model: "m", thinking: "high", sessionDir: "/sd", offline: true},
			resumeFile: "/tmp/x.jsonl",
			want: []string{"rpc", "--no-color",
				"--provider", "p", "--model", "m",
				"--thinking", "high",
				"--session-dir", "/sd",
				"--session-file", "/tmp/x.jsonl",
				"--offline"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := buildArgs(c.agent, c.resumeFile)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("buildArgs(%s) = %v, want %v", c.name, got, c.want)
			}
		})
	}
}

func TestBuildArgs_NoNoConfirmFlag(t *testing.T) {
	// Regression: cc-connect MUST NEVER pass --no-confirm.
	a := &Agent{provider: "p", model: "m", thinking: "xhigh", offline: true, sessionDir: "/d"}
	for _, arg := range buildArgs(a, "/x.jsonl") {
		if arg == "--no-confirm" {
			t.Fatalf("buildArgs leaked --no-confirm; full args: %v", buildArgs(a, "/x.jsonl"))
		}
	}
}

func TestPermissionModes_Keys(t *testing.T) {
	a := &Agent{}
	keys := map[string]bool{}
	for _, m := range a.PermissionModes() {
		keys[m.Key] = true
	}
	for _, want := range []string{"default", "dontAsk", "bypassPermissions", "yolo"} {
		if !keys[want] {
			t.Errorf("PermissionModes missing %q", want)
		}
	}
}

func TestStripANSI(t *testing.T) {
	in := "\x1b[31mred\x1b[0m text"
	if got := stripANSI(in); got != "red text" {
		t.Errorf("stripANSI = %q", got)
	}
}

func TestCollapseBlankLines(t *testing.T) {
	in := "a\n\n\n\nb\n\nc"
	if got := collapseBlankLines(in); got != "a\n\nb\n\nc" {
		t.Errorf("collapseBlankLines = %q", got)
	}
}

func TestSanitizeFileName(t *testing.T) {
	if got := sanitizeFileName("../../etc/passwd"); got != "passwd" {
		t.Errorf("sanitizeFileName = %q", got)
	}
	if got := sanitizeFileName(""); got != "" {
		t.Errorf("sanitizeFileName empty = %q", got)
	}
}

// ── New / configuration ────────────────────────────────────

func TestNew_ProviderRequiresModel(t *testing.T) {
	// cmd must exist in PATH for the cmd check to succeed; use /bin/sh.
	_, err := New(map[string]any{"cmd": "/bin/sh", "provider": "yonyou"})
	if err == nil {
		t.Fatal("provider without model should error")
	}
	if !strings.Contains(err.Error(), "requires model") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestNew_CmdNotFound(t *testing.T) {
	_, err := New(map[string]any{"cmd": "definitely-not-on-path-xyz123"})
	if err == nil {
		t.Fatal("missing cmd should error")
	}
	if !strings.Contains(err.Error(), "not found in PATH") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestNew_Defaults(t *testing.T) {
	a, err := New(map[string]any{"cmd": "/bin/sh"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	agent := a.(*Agent)
	if agent.workDir != "." {
		t.Errorf("workDir default = %q", agent.workDir)
	}
	if agent.mode != "default" {
		t.Errorf("mode default = %q", agent.mode)
	}
	if agent.confirmTimeout.Seconds() != 300 {
		t.Errorf("confirmTimeout default = %v", agent.confirmTimeout)
	}
}

func TestNew_FloatTimeoutFromTOML(t *testing.T) {
	// TOML parsers often unmarshal ints as float64.
	a, err := New(map[string]any{"cmd": "/bin/sh", "confirm_timeout_secs": float64(42)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := a.(*Agent).confirmTimeout.Seconds(); got != 42 {
		t.Errorf("confirmTimeout = %v, want 42s", got)
	}
}

// ── resolveResumeFile (§B6 — never fall through) ──────────

func TestResolveResumeFile_NoSessionID_NoConfig(t *testing.T) {
	a := &Agent{}
	got, err := a.resolveResumeFile("")
	if err != nil || got != "" {
		t.Fatalf("got (%q, %v), want ('', nil)", got, err)
	}
	got2, err2 := a.resolveResumeFile(core.ContinueSession)
	if err2 != nil || got2 != "" {
		t.Fatalf("continue: got (%q, %v), want ('', nil)", got2, err2)
	}
}

func TestResolveResumeFile_SessionFileConfigured_Exists(t *testing.T) {
	dir := t.TempDir()
	sf := filepath.Join(dir, "saved.jsonl")
	if err := os.WriteFile(sf, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &Agent{sessionFile: sf}
	got, err := a.resolveResumeFile("")
	if err != nil || got != sf {
		t.Fatalf("got (%q, %v), want (%q, nil)", got, err, sf)
	}
}

func TestResolveResumeFile_SessionFileConfigured_Missing(t *testing.T) {
	a := &Agent{sessionFile: "/no/such/file.jsonl"}
	_, err := a.resolveResumeFile("")
	if err == nil {
		t.Fatal("expected error for missing session_file")
	}
}

func TestResolveResumeFile_SessionIDFound(t *testing.T) {
	dir := t.TempDir()
	// File named per yms-rca convention: <timestamp>_<uuid>.jsonl
	path := filepath.Join(dir, "20260101_abc-123.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &Agent{sessionDir: dir}
	got, err := a.resolveResumeFile("abc-123")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != path {
		t.Errorf("got %q, want %q", got, path)
	}
}

// ── Trivial accessors / Agent surface ─────────────────────

func TestAgent_Accessors(t *testing.T) {
	a, err := New(map[string]any{"cmd": "/bin/sh", "work_dir": "/tmp/x"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	agent := a.(*Agent)

	if a.Name() != "yms-rca" {
		t.Errorf("Name = %q", a.Name())
	}
	if err := a.Stop(); err != nil {
		t.Errorf("Stop: %v", err)
	}
	agent.SetSessionEnv([]string{"X=1"})
	if agent.CLIBinaryName() != "/bin/sh" {
		t.Errorf("CLIBinaryName = %q", agent.CLIBinaryName())
	}
	if agent.CLIDisplayName() != "yms-rca" {
		t.Errorf("CLIDisplayName = %q", agent.CLIDisplayName())
	}

	agent.SetMode("yolo")
	if agent.GetMode() != "yolo" {
		t.Errorf("GetMode = %q", agent.GetMode())
	}
	agent.SetModel("foo/bar")
	if agent.GetModel() != "foo/bar" {
		t.Errorf("GetModel = %q", agent.GetModel())
	}
	if got := agent.AvailableModels(nil); got != nil {
		t.Errorf("AvailableModels should be nil, got %+v", got)
	}

	agent.SetReasoningEffort("xhigh")
	if agent.GetReasoningEffort() != "xhigh" {
		t.Errorf("GetReasoningEffort = %q", agent.GetReasoningEffort())
	}
	if len(agent.AvailableReasoningEfforts()) == 0 {
		t.Error("AvailableReasoningEfforts empty")
	}

	agent.SetWorkDir("/tmp/y")
	if agent.GetWorkDir() != "/tmp/y" {
		t.Errorf("GetWorkDir = %q", agent.GetWorkDir())
	}

	if !strings.HasSuffix(agent.ProjectMemoryFile(), "/AGENTS.md") {
		t.Errorf("ProjectMemoryFile = %q", agent.ProjectMemoryFile())
	}
	if g := agent.GlobalMemoryFile(); !strings.HasSuffix(g, ".pi/agent/AGENTS.md") {
		t.Errorf("GlobalMemoryFile = %q (must be ~/.pi/agent/AGENTS.md per design)", g)
	}
}

func TestAgent_SetSessionEnvDefaultsYMSRCASurfaceToChat(t *testing.T) {
	unsetEnvForTest(t, "YMS_RCA_SURFACE")
	a, err := New(map[string]any{"cmd": "/bin/sh", "work_dir": "/tmp/x"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	agent := a.(*Agent)
	agent.SetSessionEnv([]string{
		"CC_PROJECT=demo",
		"CC_SESSION_KEY=youzone:conv:user",
	})
	agent.mu.Lock()
	extraEnv := append([]string(nil), agent.sessionEnv...)
	agent.mu.Unlock()
	env := buildSessionEnv(extraEnv)
	if got := envValue(env, "YMS_RCA_SURFACE"); got != "chat" {
		t.Fatalf("YMS_RCA_SURFACE = %q, want chat", got)
	}
}

func TestAgent_SetSessionEnvKeepsExplicitYMSRCASurface(t *testing.T) {
	a, err := New(map[string]any{"cmd": "/bin/sh", "work_dir": "/tmp/x"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	agent := a.(*Agent)
	agent.SetSessionEnv([]string{
		"CC_PROJECT=demo",
		"YMS_RCA_SURFACE=terminal",
	})
	agent.mu.Lock()
	extraEnv := append([]string(nil), agent.sessionEnv...)
	agent.mu.Unlock()
	env := buildSessionEnv(extraEnv)
	if got := envValue(env, "YMS_RCA_SURFACE"); got != "terminal" {
		t.Fatalf("YMS_RCA_SURFACE = %q, want terminal", got)
	}
}

func TestAgent_SetSessionEnvPreservesProcessYMSRCASurfaceWhenBuildingSessionEnv(t *testing.T) {
	t.Setenv("YMS_RCA_SURFACE", "terminal")
	a, err := New(map[string]any{"cmd": "/bin/sh", "work_dir": "/tmp/x"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	agent := a.(*Agent)
	agent.SetSessionEnv([]string{
		"CC_PROJECT=demo",
		"CC_SESSION_KEY=youzone:conv:user",
	})
	agent.mu.Lock()
	extraEnv := append([]string(nil), agent.sessionEnv...)
	agent.mu.Unlock()

	env := buildSessionEnv(extraEnv)
	if got := envValue(env, "YMS_RCA_SURFACE"); got != "terminal" {
		t.Fatalf("YMS_RCA_SURFACE = %q, want terminal", got)
	}
}

func unsetEnvForTest(t *testing.T, key string) {
	t.Helper()
	oldValue, hadOldValue := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
	t.Cleanup(func() {
		if hadOldValue {
			_ = os.Setenv(key, oldValue)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return strings.TrimPrefix(kv, prefix)
		}
	}
	return ""
}

// Regression for code-review MEDIUM: yms-rca has many constructor-only
// fields (cmd, provider, thinking, session_dir, offline,
// confirm_timeout_secs). Without WorkspaceAgentOptions, the engine would
// silently drop them when re-creating the agent for another workspace.
func TestAgent_WorkspaceAgentOptions_PreservesAllFields(t *testing.T) {
	a, err := New(map[string]any{
		"cmd":                  "/bin/sh",
		"work_dir":             "/tmp/orig",
		"model":                "yonyou/MiniMax",
		"thinking":             "high",
		"mode":                 "yolo",
		"session_dir":          "/tmp/sd",
		"session_file":         "",
		"offline":              true,
		"confirm_timeout_secs": 42,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	snap := a.(*Agent).WorkspaceAgentOptions()

	// work_dir must NOT leak — the engine sets it per workspace.
	if _, ok := snap["work_dir"]; ok {
		t.Error("WorkspaceAgentOptions leaked work_dir; engine contract is to set it per workspace")
	}

	expect := map[string]any{
		"cmd":                  "/bin/sh",
		"model":                "yonyou/MiniMax",
		"thinking":             "high",
		"mode":                 "yolo",
		"session_dir":          "/tmp/sd",
		"offline":              true,
		"confirm_timeout_secs": 42,
	}
	for k, want := range expect {
		got, ok := snap[k]
		if !ok {
			t.Errorf("WorkspaceAgentOptions missing %q", k)
			continue
		}
		if got != want {
			t.Errorf("WorkspaceAgentOptions[%q] = %v, want %v", k, got, want)
		}
	}
}

// Round-trip: feeding WorkspaceAgentOptions() (plus work_dir) back into
// New() reproduces an equivalent agent — proves the engine's recreate path
// will be functional for bound workspaces.
func TestAgent_WorkspaceAgentOptions_RoundTripsThroughNew(t *testing.T) {
	original, err := New(map[string]any{
		"cmd":                  "/bin/sh",
		"work_dir":             "/tmp/a",
		"provider":             "p",
		"model":                "p/m",
		"thinking":             "low",
		"mode":                 "dontAsk",
		"offline":              true,
		"confirm_timeout_secs": 60,
	})
	if err != nil {
		t.Fatalf("New(original): %v", err)
	}
	opts := original.(*Agent).WorkspaceAgentOptions()
	opts["work_dir"] = "/tmp/b" // engine fills this in

	clone, err := New(opts)
	if err != nil {
		t.Fatalf("New(clone): %v", err)
	}
	c := clone.(*Agent)
	if c.cmd != "/bin/sh" || c.provider != "p" || c.model != "p/m" ||
		c.thinking != "low" || c.mode != "dontAsk" || !c.offline ||
		c.confirmTimeout.Seconds() != 60 || c.workDir != "/tmp/b" {
		t.Errorf("clone fields wrong: %+v", c)
	}
}

func TestAgent_StartSession_InvalidSessionID(t *testing.T) {
	dir := t.TempDir()
	a, err := New(map[string]any{"cmd": "/bin/sh", "session_dir": dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// No fallback — must error, not spawn a subprocess on missing id.
	_, err = a.StartSession(t.Context(), "no-such-id")
	if err == nil {
		t.Fatal("expected error from missing sessionID; got nil")
	}
}

func TestResolveResumeFile_SessionIDNotFound_NoFallback(t *testing.T) {
	dir := t.TempDir()
	// Configure sessionFile — even though it exists, sessionID-not-found must
	// NOT fall through to it (§B6 hard rule).
	sf := filepath.Join(dir, "fallback.jsonl")
	if err := os.WriteFile(sf, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &Agent{sessionDir: dir, sessionFile: sf}
	_, err := a.resolveResumeFile("nonexistent-uuid")
	if err == nil {
		t.Fatal("expected error when sessionID not found; falling back to sessionFile is a regression")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("unexpected error: %v", err)
	}
}
