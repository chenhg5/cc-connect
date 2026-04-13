//go:build !windows

package core

// runas_check.go — startup-time preflight gates for run_as_user.
//
// These are the hard go/no-go checks described in issue #496. They are
// intentionally more expensive than VerifyRunAsUserCheap (which only runs
// the two sudo probes) because they also touch the filesystem and walk the
// project's work_dir looking for permission problems the target user would
// hit at runtime.
//
// Use PreflightRunAsUser at cc-connect startup, in parallel across all
// projects, and refuse to start the daemon if any project returns any
// fatal error. Warnings are surfaced via slog but do not abort startup.
//
// Tests stub the SudoRunner so this file has no tie to an actual sudo
// binary.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// PreflightResult is the outcome of PreflightRunAsUser for one project.
type PreflightResult struct {
	Project   string
	RunAsUser string
	Fatal     []error
	Warnings  []string
	// SudoListOutput is captured from `sudo -n -l` when check 2 fails,
	// so the fatal message can point at the offending sudoers rule.
	SudoListOutput string
}

func (r PreflightResult) HasFatal() bool { return len(r.Fatal) > 0 }

type DescendantScanConfig struct {
	PrunePaths []string
	MaxReport  int
	Timeout    time.Duration
}

// DefaultDescendantScanConfig is the baseline used unless a caller
// overrides it.
var DefaultDescendantScanConfig = DescendantScanConfig{
	PrunePaths: []string{
		".git", "node_modules", ".venv", "venv", "dist", "build",
		"target", ".pytest_cache", "__pycache__", ".next", ".cache",
	},
	MaxReport: 50,
	Timeout:   10 * time.Second,
}

type PreflightConfig struct {
	Project    string
	RunAsUser  string
	WorkDir    string
	Runner     SudoRunner
	ScanConfig DescendantScanConfig
}

// PreflightRunAsUser runs all three startup safety checks for a single
// project. It never panics and never returns nil; instead all problems are
// accumulated into the returned PreflightResult for the caller to aggregate
// and log.
//
// Checks:
//
//  1. Passwordless sudo -iu <target> is configured (fatal if missing).
//  2. Target user has no passwordless sudo (fatal if they can escalate);
//     on failure, captures `sudo -n -iu target -- sudo -n -l` output to
//     help the operator find the offending rule.
//  3. Target user can read AND write the work_dir root (fatal if not),
//     plus a best-effort descendant walk producing warnings for paths
//     the target user cannot access.
func PreflightRunAsUser(ctx context.Context, cfg PreflightConfig) PreflightResult {
	result := PreflightResult{Project: cfg.Project, RunAsUser: cfg.RunAsUser}
	if cfg.RunAsUser == "" {
		result.Fatal = append(result.Fatal, errors.New("PreflightRunAsUser: RunAsUser is empty"))
		return result
	}
	if cfg.Runner == nil {
		cfg.Runner = ExecSudoRunner{}
	}
	if cfg.ScanConfig.MaxReport == 0 {
		cfg.ScanConfig = DefaultDescendantScanConfig
	}

	if _, err := cfg.Runner.Run(ctx, "-n", "-iu", cfg.RunAsUser, "--", "/bin/true"); err != nil {
		result.Fatal = append(result.Fatal, fmt.Errorf(
			"project %q: passwordless sudo to user %q is not configured. Add a sudoers rule such as:\n  %s ALL=(%s) NOPASSWD: ALL\nthen restart cc-connect. Underlying error: %w",
			cfg.Project, cfg.RunAsUser, currentUsernameOr("<supervisor>"), cfg.RunAsUser, err))
		return result // subsequent checks are pointless
	}

	if _, err := cfg.Runner.Run(ctx, "-n", "-iu", cfg.RunAsUser, "--", "sudo", "-n", "/bin/true"); err == nil {
		// Escalation succeeded — collect sudo -l from the target's
		// context to help the operator find the offending rule.
		if out, listErr := cfg.Runner.Run(ctx, "-n", "-iu", cfg.RunAsUser, "--", "sudo", "-n", "-l"); listErr == nil {
			result.SudoListOutput = strings.TrimSpace(string(out))
		}
		msg := fmt.Sprintf(
			"project %q: target user %q can run passwordless sudo. The run_as_user sandbox provides no isolation if the spawned agent can escalate non-interactively. Remove NOPASSWD sudo access for this user before starting cc-connect.",
			cfg.Project, cfg.RunAsUser)
		if result.SudoListOutput != "" {
			msg += "\n\n`sudo -n -l` as " + cfg.RunAsUser + ":\n" + indent(result.SudoListOutput, "  ")
		}
		result.Fatal = append(result.Fatal, errors.New(msg))
		// Don't return — still run check 3 so the operator gets all
		// the bad news in a single startup attempt.
	}

	if cfg.WorkDir == "" {
		result.Warnings = append(result.Warnings, fmt.Sprintf(
			"project %q: no work_dir configured; skipping filesystem access checks", cfg.Project))
	} else {
		absWorkDir := cfg.WorkDir
		if abs, err := filepath.Abs(absWorkDir); err == nil {
			absWorkDir = abs
		}
		if _, err := cfg.Runner.Run(ctx, "-n", "-iu", cfg.RunAsUser, "--", "test", "-r", absWorkDir, "-a", "-w", absWorkDir); err != nil {
			result.Fatal = append(result.Fatal, fmt.Errorf(
				"project %q: target user %q cannot read AND write work_dir %q. Agents will fail with EACCES at runtime. Fix ownership/permissions on this directory (chown/chmod or an ACL granting the target user rwx) before starting cc-connect.",
				cfg.Project, cfg.RunAsUser, absWorkDir))
		} else {
			warn := scanDescendants(ctx, cfg.Runner, cfg.RunAsUser, absWorkDir, cfg.ScanConfig)
			if warn != "" {
				result.Warnings = append(result.Warnings, warn)
			}
		}
	}

	return result
}

// scanDescendants runs find as the target user under workDir and
// returns a formatted warning string, or "" if nothing is flagged.
// Respects ScanConfig.Timeout. Output format per line is
// "MODE<TAB>PATH" where MODE is noread / nowrite / nosearch.
func scanDescendants(ctx context.Context, runner SudoRunner, target, workDir string, scan DescendantScanConfig) string {
	scanCtx, cancel := context.WithTimeout(ctx, scan.Timeout)
	defer cancel()

	var prune []string
	for _, p := range scan.PrunePaths {
		if len(prune) > 0 {
			prune = append(prune, "-o")
		}
		prune = append(prune, "-name", p)
	}
	// find <workDir> \( <prune exprs> \) -prune -o \( -not -readable -printf "noread\t%p\n" , -type f -not -writable -printf "nowrite\t%p\n" , -type d -not -executable -printf "nosearch\t%p\n" \) -print
	args := []string{
		"-n", "-iu", target, "--",
		"find", workDir,
	}
	if len(prune) > 0 {
		args = append(args, "(")
		args = append(args, prune...)
		args = append(args, ")", "-prune", "-o")
	}
	args = append(args,
		"(",
		"-not", "-readable", "-printf", `noread\t%p\n`,
		",",
		"-type", "f", "-not", "-writable", "-printf", `nowrite\t%p\n`,
		",",
		"-type", "d", "-not", "-executable", "-printf", `nosearch\t%p\n`,
		")",
	)

	out, err := runner.Run(scanCtx, args...)
	if scanCtx.Err() == context.DeadlineExceeded {
		return fmt.Sprintf("work_dir descendant scan timed out after %s (large repo?); skipping detailed access audit. Run `cc-connect doctor user-isolation` manually if you need it.", scan.Timeout)
	}
	// find exits non-zero if it couldn't stat some path — that's
	// actually data for us, parse whatever it printed.
	_ = err

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
		return ""
	}

	seen := make(map[string]struct{}, len(lines))
	var uniq []string
	for _, l := range lines {
		if l == "" {
			continue
		}
		if _, ok := seen[l]; ok {
			continue
		}
		seen[l] = struct{}{}
		uniq = append(uniq, l)
	}
	sort.Strings(uniq)

	if len(uniq) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "work_dir %q contains paths that user %q may not access cleanly:\n", workDir, target)
	shown := 0
	for _, l := range uniq {
		if shown >= scan.MaxReport {
			break
		}
		fmt.Fprintf(&b, "  %s\n", l)
		shown++
	}
	if len(uniq) > scan.MaxReport {
		fmt.Fprintf(&b, "  ... and %d more\n", len(uniq)-scan.MaxReport)
	}
	b.WriteString("\nThe agent may fail with EACCES when accessing these paths. Fix ownership/permissions, narrow the project scope, or accept the risk if the inaccessible paths are intentionally out of bounds.")
	return b.String()
}

func currentUsernameOr(fallback string) string {
	if u := currentUsername(); u != "" {
		return u
	}
	return fallback
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}
