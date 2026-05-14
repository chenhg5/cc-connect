package ymsagent

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// captureSlog redirects slog default output to a buffer for the duration
// of the test and returns its text content via getter.
func captureSlog(t *testing.T) (get func() string, restore func()) {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	slog.SetDefault(slog.New(h))
	return func() string { return buf.String() }, func() { slog.SetDefault(prev) }
}

func writeProfile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// TestNew_WarnsForMissingTokenEnvAcrossProfiles asserts that constructing
// the agent does NOT block when profiles declare env vars that are
// missing from cc-connect's process environment — it just warns per
// missing profile so the operator sees what /connect <name> will degrade.
func TestNew_WarnsForMissingTokenEnvAcrossProfiles(t *testing.T) {
	getLogs, restore := captureSlog(t)
	defer restore()

	dir := t.TempDir()
	writeProfile(t, dir, "yms-dev.yaml", "mcp:\n  token_env: IUAPYYS_MCP_TOKEN_TEST\n")
	writeProfile(t, dir, "yms-stage.yaml", "mcp:\n  token_env: IUAPYYS_STAGE_TOKEN_TEST\n")

	t.Setenv("IUAPYYS_MCP_TOKEN_TEST", "set")
	os.Unsetenv("IUAPYYS_STAGE_TOKEN_TEST")

	a, err := New(map[string]any{
		"cmd":             "/bin/sh",
		"connections_dir": dir,
	})
	if err != nil {
		t.Fatalf("New: %v (must not block on missing token_env)", err)
	}
	if a == nil {
		t.Fatal("New returned nil agent")
	}

	logs := getLogs()
	if !strings.Contains(logs, "IUAPYYS_STAGE_TOKEN_TEST") {
		t.Errorf("expected warn for IUAPYYS_STAGE_TOKEN_TEST in logs, got: %s", logs)
	}
	if !strings.Contains(logs, "yms-stage.yaml") {
		t.Errorf("expected warn to mention profile filename, got: %s", logs)
	}
	if strings.Contains(logs, "IUAPYYS_MCP_TOKEN_TEST") {
		t.Errorf("must not warn about variables that ARE set; logs=%s", logs)
	}
}

func TestNew_DoesNotLogTokenValues(t *testing.T) {
	getLogs, restore := captureSlog(t)
	defer restore()

	dir := t.TempDir()
	writeProfile(t, dir, "yms-dev.yaml", "mcp:\n  token_env: MY_SECRET_TOKEN_ENV\n")
	secret := "super-secret-token-VALUE-12345"
	t.Setenv("MY_SECRET_TOKEN_ENV", secret)

	_, err := New(map[string]any{"cmd": "/bin/sh", "connections_dir": dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if strings.Contains(getLogs(), secret) {
		t.Fatal("token value leaked into slog output")
	}
}

func TestAgent_validateConnectionTokenEnv(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "yms-dev.yaml", "mcp:\n  token_env: IUAPYYS_MCP_TOKEN_OK\n")
	writeProfile(t, dir, "yms-stage.yaml", "mcp:\n  token_env: IUAPYYS_STAGE_TOKEN_NOPE\n")
	writeProfile(t, dir, "yms-noenv.yaml", "mcp:\n  endpoint: http://x\n")
	writeProfile(t, dir, "yms-bad.yaml", "mcp:\n  token_env: \"BAD NAME\"\n")

	t.Setenv("IUAPYYS_MCP_TOKEN_OK", "1")
	os.Unsetenv("IUAPYYS_STAGE_TOKEN_NOPE")

	a := &Agent{connectionsDir: dir}

	// connection profile exists, env is set → OK
	if err := a.validateConnectionTokenEnv("yms-dev"); err != nil {
		t.Errorf("yms-dev: %v", err)
	}
	// connection profile exists, env is unset → error mentioning env + profile
	err := a.validateConnectionTokenEnv("yms-stage")
	if err == nil {
		t.Fatal("yms-stage should error when token env is missing")
	}
	msg := err.Error()
	if !strings.Contains(msg, "IUAPYYS_STAGE_TOKEN_NOPE") || !strings.Contains(msg, "yms-stage.yaml") {
		t.Errorf("error must name env and profile: %s", msg)
	}
	// connection profile exists, no token_env declared → no error
	if err := a.validateConnectionTokenEnv("yms-noenv"); err != nil {
		t.Errorf("yms-noenv: %v", err)
	}
	// profile missing → no error (yms-rca itself will surface the missing connection)
	if err := a.validateConnectionTokenEnv("does-not-exist"); err != nil {
		t.Errorf("missing profile should not be a token-env error: %v", err)
	}
	// invalid env name → error
	if err := a.validateConnectionTokenEnv("yms-bad"); err == nil {
		t.Error("yms-bad should error for invalid env name")
	}
}

// TestSendValidatesConnectTarget covers the Send() pre-check on /connect.
// We exercise it via a stub session so we don't need a real subprocess.
func TestSendValidatesConnectTarget(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "yms-dev.yaml", "mcp:\n  token_env: TOK_OK_FOR_DEV\n")
	writeProfile(t, dir, "yms-stage.yaml", "mcp:\n  token_env: TOK_MISSING_FOR_STAGE\n")
	t.Setenv("TOK_OK_FOR_DEV", "yes")
	os.Unsetenv("TOK_MISSING_FOR_STAGE")

	makeSession := func() (*session, *mockEncoder) {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		enc := &mockEncoder{}
		s := &session{
			mode:           "default",
			cfg:            &Agent{connectionsDir: dir, confirmTimeout: 100 * time.Millisecond},
			ctx:            ctx,
			cancel:         cancel,
			events:         make(chan core.Event, 64),
			pendingConfirm: make(map[string]*pendingPermission),
			seenToolUse:    make(map[string]struct{}),
			seenToolDone:   make(map[string]struct{}),
		}
		s.alive.Store(true)
		s.encMock = enc
		s.sessionID.Store("")
		return s, enc
	}

	// /connect to a target with a satisfied token env → write goes through.
	t.Run("connect ok", func(t *testing.T) {
		s, enc := makeSession()
		if err := s.Send("/connect yms-dev", nil, nil); err != nil {
			t.Fatalf("Send: %v", err)
		}
		if len(enc.framesCopy()) != 1 {
			t.Fatalf("expected 1 frame written, got %d", len(enc.framesCopy()))
		}
	})

	// /connect to a target with missing env → Send returns structured error;
	// no frame written; busy is releasable (second call returns the same
	// validation error, not "previous turn still running").
	t.Run("connect missing env", func(t *testing.T) {
		s, enc := makeSession()
		err := s.Send("/connect yms-stage", nil, nil)
		if err == nil {
			t.Fatal("expected error for missing env on /connect yms-stage")
		}
		if !strings.Contains(err.Error(), "TOK_MISSING_FOR_STAGE") {
			t.Errorf("error must name missing env: %v", err)
		}
		if len(enc.framesCopy()) != 0 {
			t.Errorf("must not write any frame on validation failure; got %+v", enc.framesCopy())
		}
		// busy CAS must still be releasable — second call must error with the
		// same validation reason (not "previous turn still running").
		err2 := s.Send("/connect yms-stage", nil, nil)
		if err2 == nil || strings.Contains(err2.Error(), "previous turn still running") {
			t.Errorf("validation should not have left busy=true; got: %v", err2)
		}
	})

	// Same as above but the prompt arrives wrapped in the engine's
	// inject_sender header.
	t.Run("connect missing env with inject_sender", func(t *testing.T) {
		s, enc := makeSession()
		err := s.Send("[cc-connect sender_id=ou_abc platform=feishu chat_id=oc_xyz]\n/connect yms-stage", nil, nil)
		if err == nil {
			t.Fatal("expected error for missing env even when wrapped in inject_sender header")
		}
		if len(enc.framesCopy()) != 0 {
			t.Errorf("must not write frame: %+v", enc.framesCopy())
		}
	})

	// Header with sender_name containing quotes.
	t.Run("connect with sender_name quoted", func(t *testing.T) {
		s, _ := makeSession()
		err := s.Send("[cc-connect sender_id=ou_abc sender_name=\"John Doe\" platform=feishu]\n/connect yms-stage", nil, nil)
		if err == nil {
			t.Fatal("expected error for missing env with quoted sender_name header")
		}
	})

	// Non-/connect prompts must be passed through even when env is missing
	// for other profiles — the user might be talking to an already-connected
	// session.
	t.Run("non-connect prompt bypasses validation", func(t *testing.T) {
		s, enc := makeSession()
		if err := s.Send("show me logs", nil, nil); err != nil {
			t.Fatalf("Send: %v", err)
		}
		if len(enc.framesCopy()) != 1 {
			t.Fatalf("expected 1 frame written, got %d", len(enc.framesCopy()))
		}
	})

	// Inject header + non-connect prompt.
	t.Run("non-connect with inject_sender bypasses validation", func(t *testing.T) {
		s, enc := makeSession()
		if err := s.Send("[cc-connect platform=youzone]\nshow me logs", nil, nil); err != nil {
			t.Fatalf("Send: %v", err)
		}
		if len(enc.framesCopy()) != 1 {
			t.Fatalf("expected 1 frame written, got %d", len(enc.framesCopy()))
		}
	})

	// Diagnostics must NOT leak the actual token value when one is set.
	t.Run("missing-env error does not leak set tokens elsewhere", func(t *testing.T) {
		s, _ := makeSession()
		err := s.Send("/connect yms-stage", nil, nil)
		if err == nil {
			t.Fatal("expected error")
		}
		if strings.Contains(err.Error(), "yes") {
			// "yes" was the value of TOK_OK_FOR_DEV. It must not appear.
			t.Errorf("token value leaked: %v", err)
		}
	})
}

// TestBuildSessionEnv_InheritsProcessEnvAndExtras locks in the contract
// behind newSession's c.Env assignment: cc-connect's process env reaches
// yms-rca rpc (so profile-declared mcp.token_env values resolve in the
// child), session-specific extras are appended, and the NO_COLOR /
// FORCE_COLOR sentinels are always set so the JSONL stream stays clean.
func TestBuildSessionEnv_InheritsProcessEnvAndExtras(t *testing.T) {
	t.Setenv("CC_TEST_INHERIT_TOKEN", "inherited-value")
	env := buildSessionEnv([]string{"EXTRA_SESSION_KEY=session-value"})

	asMap := make(map[string]string, len(env))
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			asMap[kv[:i]] = kv[i+1:]
		}
	}
	if asMap["CC_TEST_INHERIT_TOKEN"] != "inherited-value" {
		t.Errorf("process env not inherited: %q", asMap["CC_TEST_INHERIT_TOKEN"])
	}
	if asMap["EXTRA_SESSION_KEY"] != "session-value" {
		t.Errorf("extra env not applied: %q", asMap["EXTRA_SESSION_KEY"])
	}
	if asMap["NO_COLOR"] != "1" {
		t.Errorf("NO_COLOR not set: %q", asMap["NO_COLOR"])
	}
	if asMap["FORCE_COLOR"] != "0" {
		t.Errorf("FORCE_COLOR not set: %q", asMap["FORCE_COLOR"])
	}
}

// TestSpawn_ChildSeesInheritedEnv is the integration-style counterpart:
// it really spawns /bin/sh and asserts the child observes
// cc-connect's process env (which is how IUAPYYS_MCP_TOKEN reaches
// yms-rca rpc). Skipped if /bin/sh is unavailable (Windows CI).
func TestSpawn_ChildSeesInheritedEnv(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh not available")
	}
	t.Setenv("CC_TEST_SPAWN_TOK", "spawn-inherit-value")

	outFile := filepath.Join(t.TempDir(), "env.out")
	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", "echo \"$CC_TEST_SPAWN_TOK\" > "+outFile)
	cmd.Env = buildSessionEnv(nil)
	if err := cmd.Run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "spawn-inherit-value" {
		t.Fatalf("child env = %q, want spawn-inherit-value", got)
	}
}

// keep atomic import in use even if no test directly references it
var _ = atomic.Uint32{}
