//go:build !windows

package core

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestBuildSpawnCommand_Legacy(t *testing.T) {
	ctx := context.Background()
	cmd := BuildSpawnCommand(ctx, SpawnOptions{}, "claude", "--version")
	if cmd == nil {
		t.Fatal("BuildSpawnCommand returned nil")
	}
	if cmd.Path == "" || !strings.HasSuffix(cmd.Args[0], "claude") {
		t.Fatalf("legacy spawn: want path ending in claude, got %q (args %v)", cmd.Path, cmd.Args)
	}
	if len(cmd.Args) != 2 || cmd.Args[1] != "--version" {
		t.Fatalf("legacy spawn: unexpected args %v", cmd.Args)
	}
}

func TestBuildSpawnCommand_RunAsUser(t *testing.T) {
	ctx := context.Background()
	opts := SpawnOptions{
		RunAsUser:    "partseeker-coder",
		EnvAllowlist: []string{"PGSSLROOTCERT", "PGSSLMODE"},
	}
	cmd := BuildSpawnCommand(ctx, opts, "claude", "--version", "-p", "hello")
	if cmd == nil {
		t.Fatal("BuildSpawnCommand returned nil")
	}
	if !strings.HasSuffix(cmd.Args[0], "sudo") {
		t.Fatalf("want argv[0] ending in sudo, got %q", cmd.Args[0])
	}
	// Expected: sudo -n -iu partseeker-coder --preserve-env=<allow> -- claude --version -p hello
	want := []string{"-n", "-iu", "partseeker-coder"}
	if !reflect.DeepEqual(cmd.Args[1:4], want) {
		t.Fatalf("sudo args[1:4] = %v, want %v", cmd.Args[1:4], want)
	}
	if !strings.HasPrefix(cmd.Args[4], "--preserve-env=") {
		t.Fatalf("expected --preserve-env= at args[4], got %q", cmd.Args[4])
	}
	// Allowlist must include both defaults and the extensions, sorted+deduped.
	preserveList := strings.TrimPrefix(cmd.Args[4], "--preserve-env=")
	preserved := strings.Split(preserveList, ",")
	for _, needed := range []string{"LANG", "LC_ALL", "TERM", "PGSSLROOTCERT", "PGSSLMODE"} {
		if !slices.Contains(preserved, needed) {
			t.Errorf("preserve-env missing %q; got %v", needed, preserved)
		}
	}
	// PATH must NOT be in the allowlist — sudo -i rebuilds it from the
	// target user's login profile, and preserving the supervisor's PATH
	// would leak supervisor work dirs into the isolated session.
	if slices.Contains(preserved, "PATH") {
		t.Errorf("preserve-env leaked PATH; got %v", preserved)
	}
	if cmd.Args[5] != "--" {
		t.Fatalf("args[5] = %q, want --", cmd.Args[5])
	}
	if cmd.Args[6] != "claude" {
		t.Fatalf("args[6] = %q, want claude", cmd.Args[6])
	}
	if !reflect.DeepEqual(cmd.Args[7:], []string{"--version", "-p", "hello"}) {
		t.Fatalf("agent args = %v, want [--version -p hello]", cmd.Args[7:])
	}
}

func TestFilterEnvForSpawn_Legacy(t *testing.T) {
	env := []string{"PATH=/usr/bin", "SECRET=top", "PWD=/tmp"}
	got := FilterEnvForSpawn(env, SpawnOptions{})
	if !reflect.DeepEqual(got, env) {
		t.Fatalf("legacy mode should pass env through unchanged; got %v, want %v", got, env)
	}
}

func TestFilterEnvForSpawn_RunAsUser(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"LANG=en_US.UTF-8",
		"SECRET=top",
		"HOME=/home/supervisor",
		"SUPERVISOR_CREDENTIAL=nope",
		"PGSSLROOTCERT=/etc/certs/root.crt",
	}
	opts := SpawnOptions{
		RunAsUser:    "target",
		EnvAllowlist: []string{"PGSSLROOTCERT"},
	}
	got := FilterEnvForSpawn(env, opts)
	// LANG, PGSSLROOTCERT should survive; PATH, SECRET, HOME,
	// SUPERVISOR_CREDENTIAL must not. PATH is deliberately dropped so
	// sudo -i can rebuild it from the target user's login profile
	// instead of inheriting the supervisor's PATH.
	wantKept := map[string]bool{
		"LANG=en_US.UTF-8":                 true,
		"PGSSLROOTCERT=/etc/certs/root.crt": true,
	}
	for _, e := range got {
		if !wantKept[e] {
			t.Errorf("unexpected env survived filter: %q", e)
		}
		delete(wantKept, e)
	}
	for e := range wantKept {
		t.Errorf("expected env missing after filter: %q", e)
	}
	for _, e := range got {
		if strings.HasPrefix(e, "PATH=") || strings.HasPrefix(e, "SECRET=") || strings.HasPrefix(e, "HOME=") || strings.HasPrefix(e, "SUPERVISOR_CREDENTIAL=") {
			t.Errorf("disallowed env leaked: %q", e)
		}
	}
}

// stubSudoRunner implements SudoRunner for tests.
type stubSudoRunner struct {
	// script maps an argv slice (joined with \x1f) to a response.
	script map[string]stubResponse
	// calls records every invocation in order.
	calls [][]string
}

type stubResponse struct {
	out []byte
	err error
}

func (s *stubSudoRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	s.calls = append(s.calls, append([]string{}, args...))
	key := strings.Join(args, "\x1f")
	if r, ok := s.script[key]; ok {
		return r.out, r.err
	}
	return nil, errors.New("stubSudoRunner: unscripted call: " + strings.Join(args, " "))
}

func key(args ...string) string { return strings.Join(args, "\x1f") }

func TestVerifyRunAsUserCheap_Success(t *testing.T) {
	ResetVerifyCache()
	runner := &stubSudoRunner{
		script: map[string]stubResponse{
			key("-n", "-iu", "target", "--", "/bin/true"):                           {nil, nil},
			key("-n", "-iu", "target", "--", "sudo", "-n", "/bin/true"):             {[]byte("a password is required"), &exec.ExitError{}},
		},
	}
	if err := VerifyRunAsUserCheap(context.Background(), runner, "target"); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("want 2 sudo calls, got %d", len(runner.calls))
	}
}

func TestVerifyRunAsUserCheap_NoPasswordlessSudoToTarget(t *testing.T) {
	ResetVerifyCache()
	runner := &stubSudoRunner{
		script: map[string]stubResponse{
			key("-n", "-iu", "target", "--", "/bin/true"): {[]byte("a password is required"), &exec.ExitError{}},
		},
	}
	err := VerifyRunAsUserCheap(context.Background(), runner, "target")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "passwordless sudo to user") {
		t.Errorf("error = %v, want 'passwordless sudo to user' message", err)
	}
}

func TestVerifyRunAsUserCheap_TargetCanEscalate(t *testing.T) {
	ResetVerifyCache()
	runner := &stubSudoRunner{
		script: map[string]stubResponse{
			key("-n", "-iu", "target", "--", "/bin/true"):               {nil, nil},
			key("-n", "-iu", "target", "--", "sudo", "-n", "/bin/true"): {nil, nil}, // BAD — escalation succeeded
		},
	}
	err := VerifyRunAsUserCheap(context.Background(), runner, "target")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "can run passwordless sudo") {
		t.Errorf("error = %v, want 'can run passwordless sudo' message", err)
	}
}

func TestVerifyRunAsUserCheap_EmptyUser(t *testing.T) {
	ResetVerifyCache()
	runner := &stubSudoRunner{script: map[string]stubResponse{}}
	if err := VerifyRunAsUserCheap(context.Background(), runner, ""); err == nil {
		t.Fatal("want error for empty user")
	}
}

func TestVerifyRunAsUserCheap_CacheHit(t *testing.T) {
	ResetVerifyCache()
	runner := &stubSudoRunner{
		script: map[string]stubResponse{
			key("-n", "-iu", "target", "--", "/bin/true"):               {nil, nil},
			key("-n", "-iu", "target", "--", "sudo", "-n", "/bin/true"): {nil, &exec.ExitError{}},
		},
	}
	// First call populates the cache with 2 runner calls.
	if err := VerifyRunAsUserCheap(context.Background(), runner, "target"); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("first call: want 2 runner calls, got %d", len(runner.calls))
	}
	// Second call should be served from the cache — zero new runner calls.
	if err := VerifyRunAsUserCheap(context.Background(), runner, "target"); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("cached call made runner calls; want 2 total, got %d", len(runner.calls))
	}
}

