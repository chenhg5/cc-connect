//go:build !windows

package core

// runas_audit.go — isolation leak-audit probe for the run_as_user sandbox.
//
// The preflight gates in runas_check.go answer the question "can
// cc-connect spawn as the target user without errors?". This file
// answers the stronger question: "once the target user IS spawned, can
// it still read things it shouldn't be able to?".
//
// We do that by running a fixed shell script inside the target user's
// sudo -i session and parsing its output into a structured report. The
// script (runas_probe.sh) is embedded via //go:embed so it ships with the
// binary and can be audited with shellcheck.
//
// # Failure policy
//
// Per the spec: unexpected audit outcomes are FATAL. Specifically:
//
//   - Any CROSS_LEAKED (the target user can read another project user's
//     secrets) is fatal.
//   - Any SUPERVISOR_LEAKED (the target user can read the supervisor's
//     secrets) is fatal.
//   - WORKDIR_WRITABLE=no is fatal (already caught by preflight, but
//     we assert it here too as defense in depth).
//
// Everything else is informational and stored in the report but does
// not block startup.

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

//go:embed runas_probe.sh
var runasProbeScript []byte

// Probe output tags. These must stay in sync with runas_probe.sh — the
// shell script and the Go parser share this as their wire format.
const (
	tagBegin             = "BEGIN"
	tagEnd               = "END"
	tagID                = "ID"
	tagWhoami            = "WHOAMI"
	tagGroups            = "GROUPS"
	tagUmask             = "UMASK"
	tagPwd               = "PWD"
	tagHome              = "HOME"
	tagShell             = "SHELL"
	tagWorkDirPath       = "WORKDIR_PATH"
	tagWorkDirExists     = "WORKDIR_EXISTS"
	tagWorkDirReadable   = "WORKDIR_READABLE"
	tagWorkDirWritable   = "WORKDIR_WRITABLE"
	tagTargetHas         = "TARGET_HAS"
	tagTargetMissing     = "TARGET_MISSING"
	tagCrossDenied       = "CROSS_DENIED"
	tagCrossLeaked       = "CROSS_LEAKED"
	tagCrossMissing      = "CROSS_MISSING"
	tagCrossUnknown      = "CROSS_UNKNOWN"
	tagSupervisorDenied  = "SUPERVISOR_DENIED"
	tagSupervisorLeaked  = "SUPERVISOR_LEAKED"
	tagSupervisorMissing = "SUPERVISOR_MISSING"
)

// ProbeScript returns the embedded probe script. Doctor subcommand
// exposes this via --print-script for inspection.
func ProbeScript() []byte { return runasProbeScript }

// IsolationReport is the structured result of running the probe.
type IsolationReport struct {
	Project       string            `json:"project"`
	RunAsUser     string            `json:"run_as_user"`
	WorkDir       string            `json:"work_dir"`
	Timestamp     time.Time         `json:"timestamp"`
	Identity      IdentitySnapshot  `json:"identity"`
	WorkDirStatus WorkDirStatus     `json:"work_dir_status"`
	// TargetPaths lists existence results for files the target user is
	// supposed to have in their own home. Missing is informational —
	// runtime tools will fail, but it's an operator migration gap, not
	// a security hole.
	TargetPaths []PathStatus      `json:"target_paths"`
	CrossUser   []CrossUserResult `json:"cross_user"`
	Supervisor  []PathStatus      `json:"supervisor"`
	// Fatal lists audit-level fatal problems: any CROSS_LEAKED,
	// SUPERVISOR_LEAKED, or WORKDIR_WRITABLE=no.
	Fatal []string `json:"fatal,omitempty"`
	// ProbeVersion is the version string from the probe's BEGIN line;
	// bumped when the report schema changes.
	ProbeVersion string `json:"probe_version"`
	// RawOutput is only populated when the audit had a fatal problem,
	// to keep clean reports small.
	RawOutput string `json:"raw_output,omitempty"`
}

func (r IsolationReport) HasFatal() bool { return len(r.Fatal) > 0 }

type IdentitySnapshot struct {
	ID     string `json:"id"`
	Whoami string `json:"whoami"`
	Groups string `json:"groups"`
	Umask  string `json:"umask"`
	Pwd    string `json:"pwd"`
	Home   string `json:"home"`
	Shell  string `json:"shell"`
}

type WorkDirStatus struct {
	Path     string `json:"path"`
	Exists   bool   `json:"exists"`
	Readable bool   `json:"readable"`
	Writable bool   `json:"writable"`
}

type PathStatus struct {
	Path   string `json:"path"`
	Status string `json:"status"` // has | missing | denied | leaked
}

type CrossUserResult struct {
	OtherUser string `json:"other_user"`
	Path      string `json:"path"`
	Status    string `json:"status"` // missing | denied | leaked | unknown-user
}

// PrettyJSON marshals the report with two-space indentation for use in
// the doctor subcommand's on-disk report.
func (r IsolationReport) PrettyJSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

type AuditConfig struct {
	Project   string
	RunAsUser string
	WorkDir   string
	// OtherUsers: other run_as_user values configured in the same
	// instance, used for the cross-user denial leg of the probe.
	OtherUsers []string
	// Supervisor: the supervisor Unix username, used for the
	// supervisor-denial leg. Usually os/user.Current().Username.
	Supervisor string
	Runner     SudoRunner
	// ProbeScriptOverride, if non-nil, replaces the embedded probe
	// script. Tests use this; production always uses the embedded one.
	ProbeScriptOverride []byte
	Timeout             time.Duration
}

// RunIsolationProbe spawns the probe as the target user and parses its
// output. Does not fail on non-zero exit from the probe — whatever it
// managed to print is still parsed.
func RunIsolationProbe(ctx context.Context, cfg AuditConfig) (IsolationReport, error) {
	report := IsolationReport{
		Project:   cfg.Project,
		RunAsUser: cfg.RunAsUser,
		WorkDir:   cfg.WorkDir,
		Timestamp: time.Now().UTC(),
	}
	if cfg.RunAsUser == "" {
		return report, errors.New("RunIsolationProbe: RunAsUser is empty")
	}
	if cfg.Runner == nil {
		cfg.Runner = ExecSudoRunner{}
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 15 * time.Second
	}
	script := cfg.ProbeScriptOverride
	if script == nil {
		script = runasProbeScript
	}

	probeCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	// Build env injection: since sudo -i strips env, we pass the probe
	// inputs as SHELL VARIABLES by prepending `export` statements to the
	// script body. Values are pre-validated at config parse time so
	// shell-quoting concerns are limited, but we still quote everything.
	header := fmt.Sprintf(
		"export CC_PROBE_WORKDIR=%s\nexport CC_PROBE_OTHER_USERS=%s\nexport CC_PROBE_SUPERVISOR=%s\n",
		shellQuote(cfg.WorkDir),
		shellQuote(strings.Join(filterOtherUsers(cfg.OtherUsers, cfg.RunAsUser), " ")),
		shellQuote(cfg.Supervisor),
	)
	fullScript := append([]byte(header), script...)

	// We invoke `sudo -n -iu <user> -- /bin/sh -s` and pipe the script on
	// stdin. Using -s + stdin avoids argv-length limits and avoids ever
	// putting the script body on the command line.
	cmd := exec.CommandContext(probeCtx, "sudo",
		"-n", "-iu", cfg.RunAsUser, "--", "/bin/sh", "-s")
	cmd.Stdin = bytes.NewReader(fullScript)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// Still try to parse anything that made it out. Return the err
		// so callers can tell the probe didn't complete cleanly.
		report.RawOutput = stdout.String()
		parseProbeOutput(&report, stdout.String())
		return report, fmt.Errorf("probe exec failed: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	parseProbeOutput(&report, stdout.String())
	report.Fatal = computeAuditFatal(report)
	// RawOutput bloats the on-disk report — only keep it when something
	// went wrong so an operator can inspect what the probe actually saw.
	if report.HasFatal() {
		report.RawOutput = stdout.String()
	}
	return report, nil
}

// parseProbeOutput fills report in place. Unknown tags are ignored for
// forward compatibility with newer probe scripts.
func parseProbeOutput(report *IsolationReport, out string) {
	scanner := bufio.NewScanner(strings.NewReader(out))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		tag, rest := splitTag(line)
		switch tag {
		case tagBegin:
			if strings.HasPrefix(rest, "probe-version=") {
				report.ProbeVersion = strings.TrimPrefix(rest, "probe-version=")
			}
		case tagEnd:
		case tagID:
			report.Identity.ID = rest
		case tagWhoami:
			report.Identity.Whoami = rest
		case tagGroups:
			report.Identity.Groups = rest
		case tagUmask:
			report.Identity.Umask = rest
		case tagPwd:
			report.Identity.Pwd = rest
		case tagHome:
			report.Identity.Home = rest
		case tagShell:
			report.Identity.Shell = rest
		case tagWorkDirPath:
			report.WorkDirStatus.Path = rest
		case tagWorkDirExists:
			report.WorkDirStatus.Exists = rest == "yes"
		case tagWorkDirReadable:
			report.WorkDirStatus.Readable = rest == "yes"
		case tagWorkDirWritable:
			report.WorkDirStatus.Writable = rest == "yes"
		case tagTargetHas:
			report.TargetPaths = append(report.TargetPaths, PathStatus{Path: rest, Status: "has"})
		case tagTargetMissing:
			report.TargetPaths = append(report.TargetPaths, PathStatus{Path: rest, Status: "missing"})
		case tagCrossDenied, tagCrossLeaked, tagCrossMissing, tagCrossUnknown:
			other, path := splitTag(rest)
			status := strings.ToLower(strings.TrimPrefix(tag, "CROSS_"))
			if tag == tagCrossUnknown {
				status = "unknown-user"
				other = rest
				path = ""
			}
			report.CrossUser = append(report.CrossUser, CrossUserResult{
				OtherUser: other,
				Path:      path,
				Status:    status,
			})
		case tagSupervisorDenied:
			report.Supervisor = append(report.Supervisor, PathStatus{Path: rest, Status: "denied"})
		case tagSupervisorLeaked:
			report.Supervisor = append(report.Supervisor, PathStatus{Path: rest, Status: "leaked"})
		case tagSupervisorMissing:
			report.Supervisor = append(report.Supervisor, PathStatus{Path: rest, Status: "missing"})
		}
	}
}

func computeAuditFatal(r IsolationReport) []string {
	var fatal []string
	for _, c := range r.CrossUser {
		if c.Status == "leaked" {
			fatal = append(fatal, fmt.Sprintf(
				"project %q: target user %q can read %q belonging to user %q (CROSS_LEAKED)",
				r.Project, r.RunAsUser, c.Path, c.OtherUser))
		}
	}
	for _, s := range r.Supervisor {
		if s.Status == "leaked" {
			fatal = append(fatal, fmt.Sprintf(
				"project %q: target user %q can read supervisor path %q (SUPERVISOR_LEAKED)",
				r.Project, r.RunAsUser, s.Path))
		}
	}
	if r.WorkDirStatus.Path != "" && !r.WorkDirStatus.Writable {
		fatal = append(fatal, fmt.Sprintf(
			"project %q: target user %q cannot write work_dir %q (WORKDIR_WRITABLE=no)",
			r.Project, r.RunAsUser, r.WorkDirStatus.Path))
	}
	return fatal
}

func splitTag(line string) (string, string) {
	sp := strings.IndexByte(line, ' ')
	if sp < 0 {
		return line, ""
	}
	return line[:sp], line[sp+1:]
}

// shellQuote wraps s in POSIX single quotes, escaping embedded quotes.
// Used instead of fmt %q because the probe runs under /bin/sh.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func filterOtherUsers(others []string, self string) []string {
	out := make([]string, 0, len(others))
	for _, o := range others {
		if o == "" || o == self {
			continue
		}
		out = append(out, o)
	}
	return out
}
