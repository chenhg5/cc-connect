package dsh

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/klauspost/compress/zstd"
)

// ── normalizeMode ────────────────────────────────────────────

func TestNormalizeMode(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", "confirm"},
		{"default", "confirm"},
		{"confirm", "confirm"},
		{"CONFIRM", "confirm"},
		{"read-only", "read-only"},
		{"Read-Only", "read-only"},
		{"workspace-write", "workspace-write"},
		{"workspace", "workspace-write"},
		{"danger-full-access", "danger-full-access"},
		{"full-access", "danger-full-access"},
		{"yolo", "danger-full-access"},
		{"auto", "danger-full-access"},
		{"never", "danger-full-access"},
		{"  Yolo  ", "danger-full-access"},
		{"unknown", "confirm"},
	}
	for _, tt := range tests {
		if got := normalizeMode(tt.in); got != tt.want {
			t.Errorf("normalizeMode(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// ── Agent constructor ────────────────────────────────────────

func TestNew_DefaultValues(t *testing.T) {
	ag, err := New(map[string]any{"cmd": "echo"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	a := ag.(*Agent)
	if a.cmd != "echo" {
		t.Errorf("cmd = %q, want echo", a.cmd)
	}
	if a.workDir != "." {
		t.Errorf("workDir = %q, want .", a.workDir)
	}
	if a.mode != "confirm" {
		t.Errorf("mode = %q, want confirm", a.mode)
	}
	if a.model != "" {
		t.Errorf("model = %q, want empty", a.model)
	}
	if a.Name() != "dsh" {
		t.Errorf("Name() = %q, want dsh", a.Name())
	}
}

func TestNew_WithOptions(t *testing.T) {
	ag, err := New(map[string]any{
		"cmd":      "echo",
		"work_dir": "/tmp/proj",
		"model":    "deepseek-v4-pro",
		"mode":     "danger-full-access",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	a := ag.(*Agent)
	if a.workDir != "/tmp/proj" {
		t.Errorf("workDir = %q", a.workDir)
	}
	if a.model != "deepseek-v4-pro" {
		t.Errorf("model = %q", a.model)
	}
	if a.mode != "danger-full-access" {
		t.Errorf("mode = %q", a.mode)
	}
}

func TestNew_MissingBinary(t *testing.T) {
	_, err := New(map[string]any{"cmd": "definitely-not-a-real-binary-xyz"})
	if err == nil {
		t.Fatal("New() expected error for missing binary, got nil")
	}
}

// ── ModelSwitcher / ModeSwitcher ─────────────────────────────

func TestSetGetModel(t *testing.T) {
	ag, _ := New(map[string]any{"cmd": "echo"})
	a := ag.(*Agent)
	a.SetModel("deepseek-v4-flash")
	if got := a.GetModel(); got != "deepseek-v4-flash" {
		t.Errorf("GetModel() = %q", got)
	}
}

func TestSetModelForProvider(t *testing.T) {
	ag, _ := New(map[string]any{"cmd": "echo"})
	a := ag.(*Agent)
	a.SetModelForProvider("openrouter", "deepseek/deepseek-v4-pro")
	if got := a.GetModel(); got != "deepseek/deepseek-v4-pro" {
		t.Errorf("GetModel() = %q", got)
	}
	if got := a.GetModelProvider(); got != "openrouter" {
		t.Errorf("GetModelProvider() = %q", got)
	}
}

func TestSetGetMode(t *testing.T) {
	ag, _ := New(map[string]any{"cmd": "echo"})
	a := ag.(*Agent)
	a.SetMode("yolo")
	if got := a.GetMode(); got != "danger-full-access" {
		t.Errorf("GetMode() = %q, want danger-full-access", got)
	}
}

func TestSessionPreset(t *testing.T) {
	s, err := newDSHSession(context.Background(), "dsh", nil, "/tmp", "", "", "", "", "session-preset", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.GetPreset(); got != "" {
		t.Fatalf("initial preset = %q, want empty", got)
	}
	if err := s.SetPreset("minimal"); err != nil {
		t.Fatal(err)
	}
	if got := s.GetPreset(); got != "minimal" {
		t.Fatalf("preset = %q, want minimal", got)
	}
	if err := s.SetPreset(" "); err == nil {
		t.Fatal("SetPreset with empty name should fail")
	}
}

func TestPermissionModes(t *testing.T) {
	ag, _ := New(map[string]any{"cmd": "echo"})
	modes := ag.(*Agent).PermissionModes()
	if len(modes) != 4 {
		t.Fatalf("PermissionModes() len = %d, want 4", len(modes))
	}
	keys := map[string]bool{}
	for _, m := range modes {
		keys[m.Key] = true
	}
	for _, want := range []string{"read-only", "workspace-write", "danger-full-access", "confirm"} {
		if !keys[want] {
			t.Errorf("PermissionModes missing %q", want)
		}
	}
}

// ── AvailableModels / settings ───────────────────────────────

func TestAvailableModels_Fallback(t *testing.T) {
	t.Setenv("DSH_HOME", t.TempDir()) // empty settings.yaml
	ag, _ := New(map[string]any{"cmd": "echo"})
	models := ag.(*Agent).AvailableModels(context.Background())
	if len(models) != 2 {
		t.Fatalf("AvailableModels fallback len = %d, want 2", len(models))
	}
	if models[0].Name != "deepseek-v4-flash" || models[1].Name != "deepseek-v4-pro" {
		t.Errorf("unexpected fallback models: %+v", models)
	}
}

func TestAvailableModels_FromSettings(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "settings.yaml"), []byte(`
agent-default-model:
  provider: deepseek-official
  model: deepseek-v4-flash
  reasoningEffort: high
llm-deepseek:
  models:
    - id: deepseek-v4-flash
      name: DeepSeek-V4-Flash
    - id: deepseek-v4-pro
      name: DeepSeek-V4-Pro
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DSH_HOME", home)
	ag, _ := New(map[string]any{"cmd": "echo"})
	models := ag.(*Agent).AvailableModels(context.Background())
	if len(models) != 2 {
		t.Fatalf("AvailableModels len = %d, want 2", len(models))
	}
	if models[0].Name != "deepseek-v4-flash" || models[0].Desc != "DeepSeek-V4-Flash" {
		t.Errorf("unexpected model: %+v", models[0])
	}
}

func TestAvailableModels_FromRuntimeCatalog(t *testing.T) {
	home := t.TempDir()
	script := filepath.Join(home, "fake-dsh")
	content := "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"models\",\"models\":[{\"provider\":\"openrouter\",\"id\":\"deepseek/deepseek-v4-pro\",\"name\":\"DeepSeek V4 Pro\"},{\"provider\":\"openai\",\"id\":\"gpt-5.6-luna\",\"name\":\"GPT 5.6 Luna\"}]}'\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DSH_HOME", filepath.Join(home, "dsh-home"))
	ag, err := New(map[string]any{"cmd": script})
	if err != nil {
		t.Fatal(err)
	}
	models := ag.(*Agent).AvailableModels(context.Background())
	if len(models) != 4 {
		t.Fatalf("AvailableModels len = %d, want runtime catalog plus DeepSeek fallback: %+v", len(models), models)
	}
	if models[0].Provider != "openrouter" || models[0].Name != "deepseek/deepseek-v4-pro" {
		t.Errorf("unexpected first runtime model: %+v", models[0])
	}
	if !strings.Contains(models[1].Desc, "openai") {
		t.Errorf("runtime model description = %q, want provider label", models[1].Desc)
	}
}

func TestGetModel_FallsBackToSettings(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "settings.yaml"), []byte(`
agent-default-model:
  provider: deepseek-official
  model: deepseek-v4-pro
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DSH_HOME", home)
	ag, _ := New(map[string]any{"cmd": "echo"})
	if got := ag.(*Agent).GetModel(); got != "deepseek-v4-pro" {
		t.Errorf("GetModel() = %q, want deepseek-v4-pro", got)
	}
}

func TestAvailablePresets_FromUserRoot(t *testing.T) {
	home := t.TempDir()
	userRoot := filepath.Join(home, ".agent-presets", "minimal")
	if err := os.MkdirAll(userRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "settings.yaml"), []byte("agent-presets:\n  default: minimal\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userRoot, "agent.cordis.yml"), []byte("[]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userRoot, "preset.yml"), []byte("name: Minimal\ndescription: Small surface\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DSH_HOME", home)
	ag, err := New(map[string]any{"cmd": "echo"})
	if err != nil {
		t.Fatal(err)
	}
	presets := ag.(*Agent).AvailablePresets(context.Background())
	if len(presets) != 1 {
		t.Fatalf("presets = %+v, want one entry", presets)
	}
	if presets[0].ID != "minimal" || !presets[0].Default || presets[0].Name != "Minimal" {
		t.Fatalf("preset = %+v", presets[0])
	}
}

// ── buildArgs ────────────────────────────────────────────────

func TestBuildArgs(t *testing.T) {
	s, err := newDSHSession(context.Background(), "dsh", nil, "/tmp", "", "", "confirm", "codex", "session-abc", nil)
	if err != nil {
		t.Fatal(err)
	}
	args := s.buildArgs("hello world")
	want := []string{"--profile", "headless", "--session-id", "session-abc", "--mode", "confirm", "--preset", "codex", "--jsonl", "hello world"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("buildArgs() = %v, want %v", args, want)
	}
}

func TestBuildArgs_WithModel(t *testing.T) {
	s, err := newDSHSession(context.Background(), "dsh", nil, "/tmp", "", "deepseek-v4-pro", "danger-full-access", "", "session-abc", nil)
	if err != nil {
		t.Fatal(err)
	}
	args := s.buildArgs("task")
	joined := strings.Join(args, " ")
	for _, want := range []string{"--profile", "headless", "--session-id", "session-abc", "--model", "deepseek-v4-pro", "--mode", "danger-full-access", "task"} {
		if !strings.Contains(joined, want) {
			t.Errorf("buildArgs() = %v missing %q", args, want)
		}
	}
}

func TestBuildArgs_WithProvider(t *testing.T) {
	s, err := newDSHSession(context.Background(), "dsh", nil, "/tmp", "openrouter", "deepseek/deepseek-v4-pro", "", "", "session-abc", nil)
	if err != nil {
		t.Fatal(err)
	}
	args := s.buildArgs("task")
	joined := strings.Join(args, " ")
	for _, want := range []string{"--provider openrouter", "--model deepseek/deepseek-v4-pro"} {
		if !strings.Contains(joined, want) {
			t.Errorf("buildArgs() = %v missing %q", args, want)
		}
	}
}

func TestSessionID_GeneratedWhenEmpty(t *testing.T) {
	s, err := newDSHSession(context.Background(), "dsh", nil, "/tmp", "", "", "", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	id := s.CurrentSessionID()
	if id == "" {
		t.Fatal("generated session id is empty")
	}
	if !strings.HasPrefix(id, "session-cc-connect-") {
		t.Errorf("generated session id = %q, want session-cc-connect- prefix", id)
	}
}

// ── Send with a fake dsh script ──────────────────────────────

// fakeDSHScript writes a shell script that acts like the patched headless
// runner in --jsonl mode: it dumps its full argv to $1 (an env-provided
// file), emits streaming JSONL events, and (optionally) handles an approval
// request over stdin. When wantErr is set it exits non-zero with a message
// on stderr instead.
//
// JSONL emitted: text deltas, a tool call, a tool result, the final result
// envelope and the done envelope. When wantApproval is set it emits an
// approval/request line and echoes the first stdin line back to stdout
// before finishing.
func fakeDSHScript(t *testing.T, argvFile string, wantErr, wantApproval bool) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "fake-dsh.sh")
	approval := ``
	if wantApproval {
		approval = `echo '{"type":"approval/request","id":"ap-1","toolName":"bash","reason":"approve bash"}' 
read -r RESPONSE
echo "got:$RESPONSE"
`
	}
	body := `#!/bin/sh
echo "$@" > "` + argvFile + `"
if [ "` + fmt.Sprint(wantErr) + `" = "true" ]; then
  echo "dsh: BOOM: test failure" >&2
  exit 1
fi
echo '{"type":"text","text":"hel"}'
echo '{"type":"text","text":"lo"}'
echo '{"type":"thinking","text":"thinking about it"}'
echo '{"type":"tool/call","callId":"c1","name":"bash","arguments":"{\"command\":\"ls\"}"}'
echo '{"type":"tool/result","callId":"c1","name":"bash","content":"ok"}'
` + approval + `echo '{"type":"result","text":"final answer"}'
echo '{"type":"done","success":true}'
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func TestSend_Success(t *testing.T) {
	argvFile := filepath.Join(t.TempDir(), "argv.txt")
	script := fakeDSHScript(t, argvFile, false, false)

	s, err := newDSHSession(context.Background(), script, nil, t.TempDir(), "", "deepseek-v4-flash", "confirm", "", "session-test-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	done := make(chan error, 1)
	go func() { done <- s.Send("please reply", "msg-1", nil, nil) }()

	var texts []string
	var thinks []string
	var tools []string
	var result *core.Event
	timeout := time.After(15 * time.Second)
loop:
	for {
		select {
		case evt := <-s.Events():
			switch evt.Type {
			case core.EventText:
				texts = append(texts, evt.Content)
			case core.EventThinking:
				thinks = append(thinks, evt.Content)
			case core.EventToolUse:
				tools = append(tools, evt.ToolName)
			case core.EventResult:
				result = &evt
				break loop
			case core.EventError:
				t.Fatalf("unexpected error event: %v", evt.Error)
			}
		case <-timeout:
			t.Fatal("timed out waiting for result")
		}
	}
	if err := <-done; err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if len(texts) != 2 || texts[0] != "hel" || texts[1] != "lo" {
		t.Errorf("EventText = %q, want streaming hel/lo", texts)
	}
	if len(thinks) != 1 || thinks[0] != "thinking about it" {
		t.Errorf("EventThinking = %q", thinks)
	}
	if len(tools) != 1 || tools[0] != "bash" {
		t.Errorf("EventToolUse = %v, want [bash]", tools)
	}
	if result == nil || !result.Done || result.SessionID != "session-test-1" || result.Content != "final answer" {
		t.Errorf("EventResult = %+v", result)
	}

	// The fake script must have been invoked with our session id + overrides.
	argv, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatal(err)
	}
	joined := string(argv)
	for _, want := range []string{"--profile", "headless", "--session-id", "session-test-1", "--model", "deepseek-v4-flash", "--mode", "confirm", "--jsonl", "please reply"} {
		if !strings.Contains(joined, want) {
			t.Errorf("fake dsh argv %q missing %q", joined, want)
		}
	}
}

func TestSend_Approval(t *testing.T) {
	argvFile := filepath.Join(t.TempDir(), "argv.txt")
	script := fakeDSHScript(t, argvFile, false, true)

	s, err := newDSHSession(context.Background(), script, nil, t.TempDir(), "", "", "confirm", "", "session-approval", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	done := make(chan error, 1)
	go func() { done <- s.Send("please reply", "msg-1", nil, nil) }()

	var result *core.Event
	var permReq *core.Event
	timeout := time.After(15 * time.Second)
loop:
	for {
		select {
		case evt := <-s.Events():
			switch evt.Type {
			case core.EventPermissionRequest:
				permReq = &evt
				// Answer the permission request (the engine would do this
				// from the Feishu card buttons).
				if err := s.RespondPermission(evt.RequestID, core.PermissionResult{Behavior: "allow"}); err != nil {
					t.Fatalf("RespondPermission error: %v", err)
				}
			case core.EventResult:
				result = &evt
				break loop
			case core.EventError:
				t.Fatalf("unexpected error event: %v", evt.Error)
			}
		case <-timeout:
			t.Fatal("timed out waiting for result")
		}
	}
	if err := <-done; err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if permReq == nil {
		t.Fatal("expected EventPermissionRequest")
		return
	}
	if permReq.ToolName != "bash" || !strings.HasPrefix(permReq.RequestID, "dsh_") {
		t.Errorf("EventPermissionRequest = %+v", permReq)
	}
	if permReq.ToolInput == "" {
		t.Error("EventPermissionRequest.ToolInput is empty")
	}
	if result == nil || !result.Done || result.Content != "final answer" {
		t.Errorf("EventResult = %+v", result)
	}
}

func TestRespondPermission_NoActiveRun(t *testing.T) {
	s, err := newDSHSession(context.Background(), "echo", nil, t.TempDir(), "", "", "", "", "session-x", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	// No run in flight: RespondPermission must be a harmless no-op.
	if err := s.RespondPermission("dsh_unknown", core.PermissionResult{Behavior: "allow"}); err != nil {
		t.Fatalf("RespondPermission on idle session: %v", err)
	}
}

func TestSend_Error(t *testing.T) {
	argvFile := filepath.Join(t.TempDir(), "argv.txt")
	script := fakeDSHScript(t, argvFile, true, false)

	s, err := newDSHSession(context.Background(), script, nil, t.TempDir(), "", "", "", "", "session-test-2", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	done := make(chan error, 1)
	go func() { done <- s.Send("boom", "msg-1", nil, nil) }()

	var sawError bool
	var result *core.Event
	timeout := time.After(15 * time.Second)
loop:
	for {
		select {
		case evt := <-s.Events():
			switch evt.Type {
			case core.EventError:
				sawError = true
			case core.EventResult:
				result = &evt
				break loop
			}
		case <-timeout:
			t.Fatal("timed out waiting for result")
		}
	}
	if err := <-done; err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if !sawError {
		t.Error("expected EventError on failing run")
	}
	if result == nil || !result.Done {
		t.Errorf("EventResult = %+v", result)
	}
}

// ── ListSessions ─────────────────────────────────────────────

func TestListDSHSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("DSH_HOME", home)
	workDir := t.TempDir()
	root := dshSessionsRoot(workDir)
	if root == "" {
		t.Fatal("dshSessionsRoot empty")
	}
	if err := os.MkdirAll(filepath.Join(root, "session-aaa"), 0o755); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := os.MkdirAll(filepath.Join(root, "session-bbb"), 0o755); err != nil {
		t.Fatal(err)
	}

	sessions, err := listDSHSessions(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("listDSHSessions len = %d, want 2", len(sessions))
	}
	// Newest first.
	if sessions[0].ID != "session-bbb" {
		t.Errorf("sessions[0].ID = %q, want session-bbb (newest first)", sessions[0].ID)
	}
}

// ── Session titles ───────────────────────────────────────────

func TestListDSHSessions_WithTitles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("DSH_HOME", home)
	workDir := t.TempDir()
	root := dshSessionsRoot(workDir)
	if root == "" {
		t.Fatal("dshSessionsRoot empty")
	}

	// Write a session with a title event (dsh stores logs zstd-compressed).
	sessDir := filepath.Join(root, "session-aaa")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	log := `{"type":"session","data":{"id":"session-aaa","version":1}}
{"type":"user/message","data":{"message":{"role":"user","content":[{"type":"text","text":"帮我配置 superpowers"}]}}}
{"type":"assistant/message","data":{"message":{"role":"assistant","content":[{"type":"text","text":"好的"}]}}}
{"type":"session/title","data":{"title":"配置superpowers到dsh"}}
`
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(log)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, "session.jsonl.zstd"), buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	sessions, err := listDSHSessions(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("listDSHSessions len = %d, want 1", len(sessions))
	}
	if sessions[0].Summary != "配置superpowers到dsh" {
		t.Errorf("Summary = %q, want title", sessions[0].Summary)
	}
	if sessions[0].MessageCount != 1 {
		t.Errorf("MessageCount = %d, want 1", sessions[0].MessageCount)
	}
}

func TestListDSHSessions_FallbackToFirstUserMessage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("DSH_HOME", home)
	workDir := t.TempDir()
	root := dshSessionsRoot(workDir)
	sessDir := filepath.Join(root, "session-bbb")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	log := `{"type":"session","data":{"id":"session-bbb","version":1}}
{"type":"user/message","data":{"message":{"role":"user","content":[{"type":"text","text":"这是一个很长很长的第一条用户消息,用来测试标题回退逻辑"}]}}}
{"type":"assistant/message","data":{"message":{"role":"assistant","content":[{"type":"text","text":"好"}]}}}
`
	var buf bytes.Buffer
	w, _ := zstd.NewWriter(&buf)
	_, _ = w.Write([]byte(log))
	_ = w.Close()
	if err := os.WriteFile(filepath.Join(sessDir, "session.jsonl.zstd"), buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	sessions, err := listDSHSessions(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("len = %d, want 1", len(sessions))
	}
	if !strings.Contains(sessions[0].Summary, "这是一个很长很长") {
		t.Errorf("Summary = %q, want first-user-message fallback", sessions[0].Summary)
	}
}
