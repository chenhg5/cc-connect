package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/timmyagentic/cc-connect-next/config"
	"github.com/timmyagentic/cc-connect-next/core"
)

// platformReadinessGrace is how long a platform may take to become usable
// before startup says out loud that it has not.
//
// Platforms open their long connection in the background, so "platform ready",
// "engine started" and "cc-connect-next is running" are all printed before the
// first connection attempt can fail. Without this the process looks healthy
// forever while no message can reach it.
const platformReadinessGrace = 30 * time.Second

// starterPlaceholderRefusal renders why cc-connect-next will not start with a
// config that still carries first-run placeholders, or "" when there are none.
//
// The message names the file, every unreplaced key, and the step that replaces
// it: a user who missed one of them reads the fix here instead of decoding a
// platform API error several seconds later.
func starterPlaceholderRefusal(configPath string, found []config.StarterPlaceholder) string {
	if len(found) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Error: %s still contains the placeholder values cc-connect-next wrote for you to replace:\n", configPath)
	for _, f := range found {
		fmt.Fprintf(&b, "  %s\n      → %s\n", f, f.Fix)
	}
	b.WriteString("\nRefusing to start: no platform can connect with these values, and a running\n")
	b.WriteString("process would report itself healthy while delivering nothing.\n")
	b.WriteString("After editing, verify the result with: cc-connect-next doctor\n")
	return b.String()
}

// workDirProblem is a configured work_dir that the agent will not be able to
// use.
type workDirProblem struct {
	Project string
	Path    string
	Reason  string
}

func (p workDirProblem) String() string {
	return fmt.Sprintf("[%s] work_dir %s %s", p.Project, p.Path, p.Reason)
}

// inspectWorkDirs reports every project whose configured work_dir cannot be
// used as one.
//
// An unset work_dir is fine (the agent decides), multi-workspace projects use
// base_dir instead, and the starter placeholder belongs to the placeholder
// check, which explains it once and better. A missing directory is a warning
// rather than a refusal: a mount that is not up yet is a real deployment, and
// the agent reports the failure per turn.
func inspectWorkDirs(cfg *config.Config) []workDirProblem {
	if cfg == nil {
		return nil
	}
	var problems []workDirProblem
	for _, proj := range cfg.Projects {
		if proj.Mode == "multi-workspace" {
			continue
		}
		workDir, _ := proj.Agent.Options["work_dir"].(string)
		workDir = strings.TrimSpace(workDir)
		if workDir == "" || workDir == config.PlaceholderWorkDir {
			continue
		}
		resolved := config.ExpandUserPath(workDir)
		info, err := os.Stat(resolved)
		switch {
		case os.IsNotExist(err):
			problems = append(problems, workDirProblem{Project: proj.Name, Path: resolved, Reason: "does not exist"})
		case err != nil:
			problems = append(problems, workDirProblem{Project: proj.Name, Path: resolved, Reason: "cannot be read: " + err.Error()})
		case !info.IsDir():
			problems = append(problems, workDirProblem{Project: proj.Name, Path: resolved, Reason: "is not a directory"})
		}
	}
	return problems
}

// platformStatusReporter is the part of an engine the readiness watchdog
// needs.
type platformStatusReporter interface {
	PlatformStatuses() []core.PlatformStatus
}

type projectPlatformReadiness struct {
	project string
	engine  platformStatusReporter
}

// unusablePlatformWarning names the platforms that cannot deliver a message,
// or "" when every platform is usable.
func unusablePlatformWarning(sources []projectPlatformReadiness) string {
	var broken []string
	for _, s := range sources {
		if s.engine == nil {
			continue
		}
		for _, status := range s.engine.PlatformStatuses() {
			if status.Usable() {
				continue
			}
			reason := "never connected"
			if status.Err != nil {
				reason = status.Err.Error()
			}
			broken = append(broken, fmt.Sprintf("%s/%s (%s)", s.project, status.Name, reason))
		}
	}
	if len(broken) == 0 {
		return ""
	}
	return fmt.Sprintf("%s cannot deliver messages — cc-connect-next stays up and keeps retrying, but nothing reaches the agent until this is fixed. Diagnose with: cc-connect-next doctor",
		strings.Join(broken, ", "))
}

// watchPlatformReadiness reports platforms that never became usable, once,
// after the grace period. The returned timer lets callers stop the check.
func watchPlatformReadiness(sources []projectPlatformReadiness, grace time.Duration) *time.Timer {
	return time.AfterFunc(grace, func() {
		if warning := unusablePlatformWarning(sources); warning != "" {
			slog.Error("platform startup incomplete", "detail", warning)
		}
	})
}
