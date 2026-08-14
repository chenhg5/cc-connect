package dsh

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
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

func TestSetGetMode(t *testing.T) {
	ag, _ := New(map[string]any{"cmd": "echo"})
	a := ag.(*Agent)
	a.SetMode("yolo")
	if got := a.GetMode(); got != "danger-full-access" {
		t.Errorf("GetMode() = %q, want danger-full-access", got)
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

// ── buildArgs ────────────────────────────────────────────────

func TestBuildArgs(t *testing.T) {
	s, err := newDSHSession(context.Background(), "dsh", nil, "/tmp", "", "confirm", "session-abc", nil)
	if err != nil {
		t.Fatal(err)
	}
	args := s.buildArgs("hello world")
	want := []string{"--profile", "headless", "--session-id", "session-abc", "--mode", "confirm", "hello world"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("buildArgs() = %v, want %v", args, want)
	}
}

func TestBuildArgs_WithModel(t *testing.T) {
	s, err := newDSHSession(context.Background(), "dsh", nil, "/tmp", "deepseek-v4-pro", "danger-full-access", "session-abc", nil)
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

func TestSessionID_GeneratedWhenEmpty(t *testing.T) {
	s, err := newDSHSession(context.Background(), "dsh", nil, "/tmp", "", "", "", nil)
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
// runner: it dumps its full argv to $1 (an env-provided file) and prints a
// canned final answer on stdout. When wantErr is set it exits non-zero with
// a message on stderr instead.
func fakeDSHScript(t *testing.T, argvFile string, wantErr bool) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "fake-dsh.sh")
	body := `#!/bin/sh
echo "$@" > "` + argvFile + `"
if [ "` + fmt.Sprint(wantErr) + `" = "true" ]; then
  echo "dsh: BOOM: test failure" >&2
  exit 1
fi
printf 'final answer line 1\nfinal answer line 2\n'
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func TestSend_Success(t *testing.T) {
	argvFile := filepath.Join(t.TempDir(), "argv.txt")
	script := fakeDSHScript(t, argvFile, false)

	s, err := newDSHSession(context.Background(), script, nil, t.TempDir(), "deepseek-v4-flash", "confirm", "session-test-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	done := make(chan error, 1)
	go func() { done <- s.Send("please reply", "msg-1", nil, nil) }()

	var texts []string
	var result *core.Event
	timeout := time.After(15 * time.Second)
loop:
	for {
		select {
		case evt := <-s.Events():
			switch evt.Type {
			case core.EventText:
				texts = append(texts, evt.Content)
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

	if len(texts) != 1 || texts[0] != "final answer line 1\nfinal answer line 2" {
		t.Errorf("EventText = %q", texts)
	}
	if result == nil || !result.Done || result.SessionID != "session-test-1" {
		t.Errorf("EventResult = %+v", result)
	}

	// The fake script must have been invoked with our session id + overrides.
	argv, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatal(err)
	}
	joined := string(argv)
	for _, want := range []string{"--profile", "headless", "--session-id", "session-test-1", "--model", "deepseek-v4-flash", "--mode", "confirm", "please reply"} {
		if !strings.Contains(joined, want) {
			t.Errorf("fake dsh argv %q missing %q", joined, want)
		}
	}
}

func TestSend_Error(t *testing.T) {
	argvFile := filepath.Join(t.TempDir(), "argv.txt")
	script := fakeDSHScript(t, argvFile, true)

	s, err := newDSHSession(context.Background(), script, nil, t.TempDir(), "", "", "session-test-2", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

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
