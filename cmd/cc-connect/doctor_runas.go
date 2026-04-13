package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

// runDoctor dispatches `cc-connect doctor ...`. Today the only subcommand
// is `user-isolation`, but this function is the growth point for future
// diagnostics.
func runDoctor(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: cc-connect doctor <subcommand>")
		fmt.Fprintln(os.Stderr, "subcommands:")
		fmt.Fprintln(os.Stderr, "  user-isolation   audit run_as_user projects and emit an isolation report")
		os.Exit(2)
	}
	switch args[0] {
	case "user-isolation":
		runDoctorUserIsolation(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown doctor subcommand %q\n", args[0])
		os.Exit(2)
	}
}

// runDoctorUserIsolation runs preflight + isolation probe for one or all
// projects that have run_as_user set, writes a JSON report per project,
// and exits 0 on full clean, 1 otherwise.
func runDoctorUserIsolation(args []string) {
	fs := flag.NewFlagSet("doctor user-isolation", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file (default: auto-discover)")
	projectFilter := fs.String("project", "", "limit audit to a single project name")
	outPath := fs.String("out", "", "path to write JSON report (default: ~/.cc-connect/audits/<timestamp>-<project>.json per project)")
	printScript := fs.Bool("print-script", false, "print the embedded probe script and exit")
	_ = fs.Parse(args)

	if *printScript {
		os.Stdout.Write(core.ProbeScript())
		return
	}

	if runtime.GOOS == "windows" {
		fmt.Fprintln(os.Stderr, "doctor user-isolation: run_as_user is not supported on Windows")
		os.Exit(1)
	}

	cfgPath := resolveConfigPath(*configPath)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config %s: %v\n", cfgPath, err)
		os.Exit(1)
	}

	// Collect projects with run_as_user set (optionally filtered).
	type pending struct {
		project   string
		runAsUser string
		workDir   string
	}
	var targets []pending
	var allUsers []string
	for _, proj := range cfg.Projects {
		if proj.RunAsUser == "" {
			continue
		}
		allUsers = append(allUsers, proj.RunAsUser)
	}
	for _, proj := range cfg.Projects {
		if proj.RunAsUser == "" {
			continue
		}
		if *projectFilter != "" && proj.Name != *projectFilter {
			continue
		}
		wd, _ := proj.Agent.Options["work_dir"].(string)
		targets = append(targets, pending{
			project:   proj.Name,
			runAsUser: proj.RunAsUser,
			workDir:   wd,
		})
	}
	if len(targets) == 0 {
		fmt.Fprintln(os.Stderr, "doctor user-isolation: no projects with run_as_user set")
		if *projectFilter != "" {
			fmt.Fprintf(os.Stderr, "  (filter: --project %q)\n", *projectFilter)
		}
		os.Exit(0)
	}

	supervisor := ""
	if u, err := user.Current(); err == nil {
		supervisor = u.Username
	}

	runner := core.ExecSudoRunner{}

	// Fan out preflight + audit per project in parallel. Each project
	// accumulates its own buffered output so the final stdout stays
	// grouped per project instead of interleaving.
	type result struct {
		project    string
		runAsUser  string
		output     strings.Builder
		exitFailed bool
	}
	results := make([]*result, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		i, t := i, t
		r := &result{project: t.project, runAsUser: t.runAsUser}
		results[i] = r
		wg.Add(1)
		go func() {
			defer wg.Done()
			runDoctorOne(context.Background(), runner, t.project, t.runAsUser, t.workDir, allUsers, supervisor, *outPath, &r.output, &r.exitFailed)
		}()
	}
	wg.Wait()

	exitCode := 0
	for _, r := range results {
		fmt.Printf("=== %s (run_as_user = %s) ===\n", r.project, r.runAsUser)
		os.Stdout.WriteString(r.output.String())
		fmt.Println()
		if r.exitFailed {
			exitCode = 1
		}
	}
	os.Exit(exitCode)
}

// runDoctorOne runs preflight + audit for a single project and writes the
// human-readable output into out. Sets *failed to true on any fatal.
func runDoctorOne(ctx context.Context, runner core.SudoRunner, project, runAsUser, workDir string, otherUsers []string, supervisor, outPathOverride string, out *strings.Builder, failed *bool) {
	pfCtx, pfCancel := context.WithTimeout(ctx, 30*time.Second)
	pf := core.PreflightRunAsUser(pfCtx, core.PreflightConfig{
		Project:   project,
		RunAsUser: runAsUser,
		WorkDir:   workDir,
		Runner:    runner,
	})
	pfCancel()

	for _, w := range pf.Warnings {
		fmt.Fprintf(out, "[WARN] %s\n", w)
	}
	for _, f := range pf.Fatal {
		fmt.Fprintf(out, "[FATAL] %s\n", f)
	}
	if pf.HasFatal() {
		*failed = true
		return
	}
	fmt.Fprintln(out, "preflight: OK")

	audCtx, audCancel := context.WithTimeout(ctx, 20*time.Second)
	report, err := core.RunIsolationProbe(audCtx, core.AuditConfig{
		Project:    project,
		RunAsUser:  runAsUser,
		WorkDir:    workDir,
		OtherUsers: otherUsers,
		Supervisor: supervisor,
		Runner:     runner,
	})
	audCancel()
	if err != nil {
		fmt.Fprintf(out, "[FATAL] probe failed to run: %v\n", err)
		*failed = true
		return
	}

	writeHumanReport(out, report)

	dest := outPathOverride
	if dest == "" {
		dir, derr := defaultAuditDir()
		if derr != nil {
			fmt.Fprintf(out, "could not determine audit output dir: %v\n", derr)
			*failed = true
			return
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintf(out, "could not create audit dir %s: %v\n", dir, err)
			*failed = true
			return
		}
		ts := report.Timestamp.Format("20060102-150405")
		dest = filepath.Join(dir, fmt.Sprintf("%s-%s.json", ts, project))
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(out, "JSON marshal failed: %v\n", err)
		*failed = true
		return
	}
	if err := os.WriteFile(dest, data, 0o600); err != nil {
		fmt.Fprintf(out, "writing %s: %v\n", dest, err)
		*failed = true
		return
	}
	fmt.Fprintf(out, "report written: %s\n", dest)

	if report.HasFatal() {
		*failed = true
	}
}

// writeHumanReport writes a compact human summary of an audit to w. The
// JSON file is the authoritative record; this is for eyeballs.
func writeHumanReport(w *strings.Builder, r core.IsolationReport) {
	fmt.Fprintf(w, "whoami         : %s\n", r.Identity.Whoami)
	fmt.Fprintf(w, "id             : %s\n", r.Identity.ID)
	fmt.Fprintf(w, "home           : %s\n", r.Identity.Home)
	fmt.Fprintf(w, "workdir        : %s (readable=%v writable=%v)\n",
		r.WorkDirStatus.Path, r.WorkDirStatus.Readable, r.WorkDirStatus.Writable)

	hasCount, missCount := 0, 0
	for _, p := range r.TargetPaths {
		if p.Status == "has" {
			hasCount++
		} else {
			missCount++
		}
	}
	fmt.Fprintf(w, "target home    : %d present, %d missing\n", hasCount, missCount)
	for _, p := range r.TargetPaths {
		if p.Status == "missing" {
			fmt.Fprintf(w, "  missing: %s\n", p.Path)
		}
	}

	denied, leaked := 0, 0
	for _, c := range r.CrossUser {
		switch c.Status {
		case "denied":
			denied++
		case "leaked":
			leaked++
		}
	}
	fmt.Fprintf(w, "cross-user     : %d denied, %d leaked\n", denied, leaked)
	for _, c := range r.CrossUser {
		if c.Status == "leaked" {
			fmt.Fprintf(w, "  LEAKED: %s can read %s (%s)\n", r.RunAsUser, c.Path, c.OtherUser)
		}
	}

	supDenied, supLeaked := 0, 0
	for _, s := range r.Supervisor {
		switch s.Status {
		case "denied":
			supDenied++
		case "leaked":
			supLeaked++
		}
	}
	fmt.Fprintf(w, "supervisor     : %d denied, %d leaked\n", supDenied, supLeaked)
	for _, s := range r.Supervisor {
		if s.Status == "leaked" {
			fmt.Fprintf(w, "  LEAKED: %s can read supervisor's %s\n", r.RunAsUser, s.Path)
		}
	}

	if r.HasFatal() {
		fmt.Fprintln(w, "audit          : FATAL")
		for _, f := range r.Fatal {
			fmt.Fprintf(w, "  %s\n", f)
		}
	} else {
		fmt.Fprintln(w, "audit          : OK")
	}
}

// defaultAuditDir returns ~/.cc-connect/audits for the supervisor user.
func defaultAuditDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cc-connect", "audits"), nil
}
