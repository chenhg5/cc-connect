package main

import (
	"context"
	"fmt"
	"log/slog"
	"os/user"
	"runtime"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

// runRunAsUserStartupChecks runs preflight gates + isolation audit for
// every project that sets run_as_user. Runs in parallel across projects.
// Fatal on any failure. Must be called BEFORE any engine is constructed.
//
// Returns nil if every project's run_as_user configuration is clean, or
// an aggregate error (with one entry per failing project) otherwise. The
// caller should os.Exit(1) on a non-nil return after logging.
//
// On Windows, this is a no-op because config validation already rejects
// run_as_user at parse time. We still call it so the wiring is in place
// for future platforms.
func runRunAsUserStartupChecks(ctx context.Context, cfg *config.Config) error {
	if runtime.GOOS == "windows" {
		return nil
	}

	// Collect projects that have run_as_user set + their work_dirs.
	type pending struct {
		project    string
		runAsUser  string
		workDir    string
		otherUsers []string
	}
	var pendingProjects []pending
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
		wd, _ := proj.Agent.Options["work_dir"].(string)
		pendingProjects = append(pendingProjects, pending{
			project:    proj.Name,
			runAsUser:  proj.RunAsUser,
			workDir:    wd,
			otherUsers: allUsers,
		})
	}
	if len(pendingProjects) == 0 {
		return nil
	}

	slog.Info("run_as_user: running startup safety checks",
		"project_count", len(pendingProjects))

	supervisor := ""
	if u, err := user.Current(); err == nil {
		supervisor = u.Username
	}

	// Fan out preflight + audit per project in parallel. Each project's
	// result is independent; we collect them all before deciding to
	// abort, so a single startup attempt shows every problem.
	type projectOutcome struct {
		project   string
		preflight core.PreflightResult
		audit     core.IsolationReport
		auditErr  error
	}
	outcomes := make([]projectOutcome, len(pendingProjects))
	var wg sync.WaitGroup
	runner := core.ExecSudoRunner{}
	for i, p := range pendingProjects {
		i, p := i, p
		wg.Add(1)
		go func() {
			defer wg.Done()
			pfCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			outcomes[i].project = p.project
			outcomes[i].preflight = core.PreflightRunAsUser(pfCtx, core.PreflightConfig{
				Project:   p.project,
				RunAsUser: p.runAsUser,
				WorkDir:   p.workDir,
				Runner:    runner,
			})
			// Only run the audit probe if preflight passed — otherwise
			// the probe will definitely fail too and the operator only
			// needs to see the preflight error.
			if outcomes[i].preflight.HasFatal() {
				return
			}
			auditCtx, aCancel := context.WithTimeout(ctx, 20*time.Second)
			defer aCancel()
			report, err := core.RunIsolationProbe(auditCtx, core.AuditConfig{
				Project:    p.project,
				RunAsUser:  p.runAsUser,
				WorkDir:    p.workDir,
				OtherUsers: p.otherUsers,
				Supervisor: supervisor,
				Runner:     runner,
			})
			outcomes[i].audit = report
			outcomes[i].auditErr = err
		}()
	}
	wg.Wait()

	// Log every outcome — warnings, fatals, and clean passes — so the
	// operator has a single visible record of what was checked.
	var fatals []error
	for _, o := range outcomes {
		for _, w := range o.preflight.Warnings {
			slog.Warn("run_as_user: preflight warning", "project", o.project, "message", w)
		}
		for _, f := range o.preflight.Fatal {
			slog.Error("run_as_user: preflight FATAL", "project", o.project, "error", f)
			fatals = append(fatals, fmt.Errorf("project %q preflight: %w", o.project, f))
		}
		if o.preflight.HasFatal() {
			continue
		}
		if o.auditErr != nil {
			slog.Error("run_as_user: isolation probe failed to run",
				"project", o.project, "error", o.auditErr)
			fatals = append(fatals, fmt.Errorf("project %q probe: %w", o.project, o.auditErr))
			continue
		}
		slog.Info("run_as_user: isolation audit completed",
			"project", o.project,
			"whoami", o.audit.Identity.Whoami,
			"workdir_writable", o.audit.WorkDirStatus.Writable,
			"target_paths", len(o.audit.TargetPaths),
			"cross_user_results", len(o.audit.CrossUser),
		)
		for _, f := range o.audit.Fatal {
			slog.Error("run_as_user: audit FATAL", "project", o.project, "error", f)
			fatals = append(fatals, fmt.Errorf("project %q audit: %s", o.project, f))
		}
	}

	if len(fatals) > 0 {
		return fmt.Errorf("run_as_user startup checks failed for %d project(s); see logs above", len(fatals))
	}
	return nil
}
