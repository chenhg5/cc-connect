package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/timmyagentic/cc-connect-next/config"
	"github.com/timmyagentic/cc-connect-next/core"
)

// runDoctor dispatches `cc-connect-next doctor ...`.
//
// With no subcommand it runs the full health check against the configuration,
// without connecting to anything. That is deliberate: the check is needed most
// when the instance cannot connect, which is exactly when the in-chat
// `/doctor` command cannot be reached.
func runDoctor(args []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "user-isolation":
			runDoctorUserIsolation(args[1:])
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown doctor subcommand %q\n\n", args[0])
			printDoctorUsage(os.Stderr)
			os.Exit(2)
		}
	}
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			printDoctorUsage(os.Stdout)
			return
		}
	}
	os.Exit(runDoctorHealthCheck(args))
}

const doctorUsage = `usage: cc-connect-next doctor [flags]
       cc-connect-next doctor user-isolation [flags]

Checks the configured agent, platforms, dependencies, and network without
connecting to any platform. Exits 1 when a check fails.

Flags:
  --config <path>    Path to config file (default: auto-discover)
  --project <name>   Check a single project

Subcommands:
  user-isolation     Audit run_as_user projects and emit an isolation report
`

func printDoctorUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, doctorUsage)
}

// runDoctorHealthCheck runs the health check and returns the process exit code.
func runDoctorHealthCheck(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	configFlag := fs.String("config", "", "path to config file (default: auto-discover)")
	projectFilter := fs.String("project", "", "limit checks to a single project name")
	_ = fs.Parse(args)

	configPath := resolveConfigPath(*configFlag)
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "doctor: cannot read config %s: %v\n", configPath, err)
		fmt.Fprintln(os.Stderr, "Run cc-connect-next once to create a starter config, or pass --config <path>.")
		return 1
	}
	if config.ConfigPath == "" {
		config.ConfigPath = configPath
	}

	fmt.Printf("config: %s\n", configPath)

	i18n := core.NewI18n(configLanguage(cfg.Language))
	ctx := context.Background()

	failed := false
	checked := 0
	for _, proj := range cfg.Projects {
		if *projectFilter != "" && proj.Name != *projectFilter {
			continue
		}
		checked++
		fmt.Printf("\n=== %s (agent: %s) ===\n", proj.Name, proj.Agent.Type)
		results := doctorProjectResults(ctx, cfg, proj, configPath)
		fmt.Println(core.FormatDoctorResults(results, i18n))
		if doctorHasFailure(results) {
			failed = true
		}
	}

	if checked == 0 {
		fmt.Fprintf(os.Stderr, "doctor: no project named %q in %s\n", *projectFilter, configPath)
		return 1
	}
	if failed {
		return 1
	}
	return 0
}

// doctorProjectResults collects every check for one project.
//
// The agent is constructed the same way startup constructs it, so an option
// the agent rejects fails here too instead of at first use. Platforms are only
// validated: doctor never opens a connection, and never reports one.
func doctorProjectResults(ctx context.Context, cfg *config.Config, proj config.ProjectConfig, configPath string) []core.DoctorCheckResult {
	results := projectConfigChecks(cfg, proj, configPath)
	platformResults := platformConfigChecks(proj)

	agent, err := core.CreateAgent(proj.Agent.Type, buildAgentOptions(cfg.DataDir, proj))
	if err != nil {
		results = append(results, core.DoctorCheckResult{
			Name:   fmt.Sprintf("Agent (%s)", proj.Agent.Type),
			Status: core.DoctorFail,
			Detail: err.Error(),
		})
		return append(results, platformResults...)
	}
	defer func() { _ = agent.Stop() }()

	return append(results, core.RunDoctorChecksWithPlatformResults(ctx, agent, platformResults)...)
}

// projectConfigChecks reports what can be judged from the configuration file
// alone: unreplaced placeholders and an unusable work directory.
func projectConfigChecks(cfg *config.Config, proj config.ProjectConfig, configPath string) []core.DoctorCheckResult {
	var results []core.DoctorCheckResult

	var placeholders []string
	for _, found := range config.FindStarterPlaceholders(cfg) {
		if found.Project != proj.Name {
			continue
		}
		placeholders = append(placeholders, fmt.Sprintf("%s is still %q — %s", found.Location(), found.Value, found.Fix))
	}
	if len(placeholders) > 0 {
		results = append(results, core.DoctorCheckResult{
			Name:   "Config File",
			Status: core.DoctorFail,
			Detail: configPath + "\n   " + strings.Join(placeholders, "\n   "),
		})
	} else {
		results = append(results, core.DoctorCheckResult{
			Name:   "Config File",
			Status: core.DoctorPass,
			Detail: configPath,
		})
	}

	results = append(results, workDirCheck(cfg, proj)...)
	return results
}

func workDirCheck(cfg *config.Config, proj config.ProjectConfig) []core.DoctorCheckResult {
	if proj.Mode == "multi-workspace" {
		return []core.DoctorCheckResult{{
			Name:   "Work Directory",
			Status: core.DoctorPass,
			Detail: "multi-workspace mode uses base_dir " + proj.BaseDir,
		}}
	}

	single := config.ProjectConfig{Name: proj.Name, Agent: proj.Agent, Mode: proj.Mode, BaseDir: proj.BaseDir}
	for _, problem := range inspectWorkDirs(&config.Config{Projects: []config.ProjectConfig{single}}) {
		return []core.DoctorCheckResult{{
			Name:   "Work Directory",
			Status: core.DoctorFail,
			Detail: problem.Path + " " + problem.Reason,
		}}
	}

	workDir, _ := proj.Agent.Options["work_dir"].(string)
	workDir = strings.TrimSpace(workDir)
	switch workDir {
	case "":
		return []core.DoctorCheckResult{{
			Name:   "Work Directory",
			Status: core.DoctorWarn,
			Detail: "not set; the agent decides where it runs",
		}}
	case config.PlaceholderWorkDir:
		// Reported by the Config File check, with the instruction attached.
		return nil
	default:
		return []core.DoctorCheckResult{{
			Name:   "Work Directory",
			Status: core.DoctorPass,
			Detail: config.ExpandUserPath(workDir),
		}}
	}
}

// platformConfigChecks validates each configured platform without contacting
// it. Validation is the platform's own: an unknown type, a missing credential,
// or a rejected option fails here exactly as it would at startup.
func platformConfigChecks(proj config.ProjectConfig) []core.DoctorCheckResult {
	if len(proj.Platforms) == 0 {
		return []core.DoctorCheckResult{{
			Name:   "Platforms",
			Status: core.DoctorFail,
			Detail: "no platform configured; add a [[projects.platforms]] section",
		}}
	}

	var results []core.DoctorCheckResult
	for _, pc := range proj.Platforms {
		name := fmt.Sprintf("Platform (%s)", pc.Type)
		opts := make(map[string]any, len(pc.Options))
		for k, v := range pc.Options {
			opts[k] = v
		}
		if err := core.ValidatePlatformOptions(pc.Type, opts); err != nil {
			results = append(results, core.DoctorCheckResult{
				Name:   name,
				Status: core.DoctorFail,
				Detail: err.Error(),
			})
			continue
		}
		results = append(results, core.DoctorCheckResult{
			Name:   name,
			Status: core.DoctorPass,
			Detail: "configured; doctor does not open a connection",
		})
	}
	return results
}

func doctorHasFailure(results []core.DoctorCheckResult) bool {
	for _, r := range results {
		if r.Status == core.DoctorFail {
			return true
		}
	}
	return false
}
