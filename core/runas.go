//go:build !windows

// Package core — runas.go provides the spawn-as-different-Unix-user primitive
// used when a project sets `run_as_user` in config.toml.
//
// # Mechanism
//
// We intentionally spawn via:
//
//	sudo -n -iu <target-user> -- <command> [args...]
//
// The flags are load-bearing and should NOT be "simplified":
//
//   - -n (non-interactive): never prompt for a password. If passwordless
//     sudo to the target user is not configured, fail loudly instead of
//     hanging on a prompt that nobody will ever see.
//
//   - -i (simulate initial login): run the target user's full login shell,
//     loading their ~/.profile / ~/.bashrc, setting HOME to their home
//     directory, and clearing the supervisor's environment. This is what
//     makes the spawned process a "real session as that user" — their
//     ~/.claude/settings.json, their PGSSL certs, their plugin state.
//
//   - -u <target-user>: the target uid. Must be a specific username; the
//     sudoers rule that allows this should be scoped to this user only,
//     not ALL.
//
//   - -- : end of sudo options. Everything after this is the command to run
//     as the target user. Prevents an argv element that starts with "-"
//     from being reinterpreted as a sudo flag.
//
// Alternatives that are NOT used, with reasons:
//
//   - setuid(): loses the target user's shell profile entirely. No
//     ~/.bashrc, no ~/.profile, no login env. Also has to be done before
//     exec, which means the supervisor process needs CAP_SETUID or to be
//     running as root — strictly worse than sudo on both fronts.
//
//   - su - <target>: interactive-only on many distros (no -c equivalent
//     for a non-shell argv), and it consults PAM differently from sudo,
//     making the "passwordless" surface harder to reason about.
//
//   - sudo -u <target> (without -i): preserves the supervisor's cwd and
//     most of its environment. This leaks the supervisor's HOME and any
//     unset-by-default env vars, which defeats the isolation story.
//
// # Environment handling
//
// When RunAsUser is set, the supervisor's environment is NOT forwarded to
// the target user. Only variables on the explicit allowlist are passed
// through via `sudo --preserve-env=VAR1,VAR2`. The default allowlist is
// intentionally minimal; anything else should live in the target user's
// own shell profile or ~/.claude/settings.json.
package core

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"os/user"
	"sort"
	"strings"
	"sync"
	"time"
)

// currentUsername returns the current Unix login name, or "" if it can't
// be determined. Used by runas_check.go when building example sudoers
// snippets in error messages.
func currentUsername() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	return u.Username
}

// DefaultEnvAllowlist is the minimal env preserved across the sudo
// boundary. Deliberately excluded:
//   - HOME / USER / LOGNAME / SHELL — sudo -i overrides them
//   - PWD — set by cmd.Dir
//   - PATH — sudo -i builds it from the target user's /etc/profile +
//     ~/.profile. Preserving the supervisor's PATH would (a) leak
//     supervisor work directories into the isolated session and
//     (b) defeat the whole point of -i by overriding the login shell's
//     PATH. If the target user needs specific binaries on PATH, put
//     them in the system PATH (e.g. /usr/local/bin symlinks) or in
//     the target user's own shell profile.
//   - anything secret
var DefaultEnvAllowlist = []string{
	"LANG",
	"LC_ALL",
	"LC_CTYPE",
	"LC_MESSAGES",
	"TERM",
}

// SpawnOptions controls how a command is spawned. Zero value = legacy
// supervisor-user spawn. Non-empty RunAsUser triggers sudo wrapping.
type SpawnOptions struct {
	RunAsUser    string
	EnvAllowlist []string // extends DefaultEnvAllowlist, not a replacement
}

func (o SpawnOptions) IsolationMode() bool {
	return o.RunAsUser != ""
}

func (o SpawnOptions) mergedAllowlist() []string {
	seen := make(map[string]struct{}, len(DefaultEnvAllowlist)+len(o.EnvAllowlist))
	for _, v := range DefaultEnvAllowlist {
		seen[v] = struct{}{}
	}
	for _, v := range o.EnvAllowlist {
		if v == "" {
			continue
		}
		seen[v] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// BuildSpawnCommand returns an *exec.Cmd that either invokes name/args
// directly (legacy) or wraps them in `sudo -n -iu <user> --preserve-env=... -- name args...`.
//
// Does NOT run the per-spawn re-check — callers should invoke
// VerifyRunAsUserCheap immediately before Start() so a sudoers edit
// between startup preflight and spawn is caught.
func BuildSpawnCommand(ctx context.Context, opts SpawnOptions, name string, args ...string) *exec.Cmd {
	if !opts.IsolationMode() {
		return exec.CommandContext(ctx, name, args...)
	}
	sudoArgs := []string{
		"-n",
		"-iu", opts.RunAsUser,
		"--preserve-env=" + strings.Join(opts.mergedAllowlist(), ","),
		"--",
		name,
	}
	sudoArgs = append(sudoArgs, args...)
	return exec.CommandContext(ctx, "sudo", sudoArgs...)
}

// FilterEnvForSpawn strips env down to the merged allowlist when
// opts.IsolationMode() is true. Belt-and-braces with sudo's own
// --preserve-env, but having cc-connect's spawn argv be the single
// source of truth keeps test assertions clean.
func FilterEnvForSpawn(env []string, opts SpawnOptions) []string {
	if !opts.IsolationMode() {
		return env
	}
	allow := opts.mergedAllowlist()
	allowSet := make(map[string]struct{}, len(allow))
	for _, v := range allow {
		allowSet[v] = struct{}{}
	}
	out := make([]string, 0, len(env))
	for _, e := range env {
		eq := strings.IndexByte(e, '=')
		if eq <= 0 {
			continue
		}
		if _, ok := allowSet[e[:eq]]; ok {
			out = append(out, e)
		}
	}
	return out
}

// SudoRunner runs `sudo <args...>` and returns combined output. Tests
// inject a stub; production uses ExecSudoRunner.
type SudoRunner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

type ExecSudoRunner struct{}

func (ExecSudoRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "sudo", args...).CombinedOutput()
}

// VerifyRunAsUserCheap runs the two cheap preflight checks that must pass
// before every spawn, not just at startup:
//
//  1. `sudo -n -iu <user> -- /bin/true` must succeed — the supervisor still
//     has passwordless sudo to the target user.
//  2. `sudo -n -iu <user> -- sudo -n /bin/true` must FAIL — the target user
//     cannot non-interactively escalate.
//
// Returns nil if both checks behave as expected. Results are cached for
// verifyCacheTTL keyed by runAsUser so rapid-fire messages don't pay the
// ~100ms cost per spawn. A failure evicts the cache immediately so the
// next spawn re-verifies fresh.
//
// The expensive checks (work_dir access, isolation probe) live in the
// preflight and audit packages and only run at startup / via `cc-connect
// doctor user-isolation`.
func VerifyRunAsUserCheap(ctx context.Context, runner SudoRunner, runAsUser string) error {
	if runAsUser == "" {
		return errors.New("VerifyRunAsUserCheap: runAsUser is empty")
	}
	if verifyCacheHit(runAsUser) {
		return nil
	}
	if out, err := runner.Run(ctx, "-n", "-iu", runAsUser, "--", "/bin/true"); err != nil {
		verifyCacheEvict(runAsUser)
		return fmt.Errorf("passwordless sudo to user %q failed (check that your sudoers rule is present and scoped to this user): %w: %s", runAsUser, err, strings.TrimSpace(string(out)))
	}
	out, err := runner.Run(ctx, "-n", "-iu", runAsUser, "--", "sudo", "-n", "/bin/true")
	if err == nil {
		verifyCacheEvict(runAsUser)
		return fmt.Errorf("target user %q can run passwordless sudo; isolation is meaningless. Remove NOPASSWD sudo for this user. Output: %s", runAsUser, strings.TrimSpace(string(out)))
	}
	verifyCacheStore(runAsUser)
	return nil
}

// verifyCacheTTL is short by design. It absorbs a burst of messages
// (one Slack user typing rapidly) while still re-verifying often enough
// that a sudoers edit during a long idle gap is caught on the next spawn.
const verifyCacheTTL = 30 * time.Second

var (
	verifyCacheMu sync.Mutex
	verifyCache   = map[string]time.Time{}
)

func verifyCacheHit(user string) bool {
	verifyCacheMu.Lock()
	defer verifyCacheMu.Unlock()
	expires, ok := verifyCache[user]
	if !ok {
		return false
	}
	if time.Now().After(expires) {
		delete(verifyCache, user)
		return false
	}
	return true
}

func verifyCacheStore(user string) {
	verifyCacheMu.Lock()
	defer verifyCacheMu.Unlock()
	verifyCache[user] = time.Now().Add(verifyCacheTTL)
}

func verifyCacheEvict(user string) {
	verifyCacheMu.Lock()
	defer verifyCacheMu.Unlock()
	delete(verifyCache, user)
}

// ResetVerifyCache clears all cached positive verification results. Used
// by tests and available for any caller that wants to force a re-check
// on the next spawn (e.g. after reloading sudoers).
func ResetVerifyCache() {
	verifyCacheMu.Lock()
	defer verifyCacheMu.Unlock()
	verifyCache = map[string]time.Time{}
}
