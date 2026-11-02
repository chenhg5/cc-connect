package comate

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"

	"github.com/chenhg5/cc-connect/core"
)

// init registers the comate agent factory so configs can select it with
// `type = "comate"` under [projects.agent].
func init() {
	core.RegisterAgent("comate", New)
}

var (
	_ core.Agent              = (*Agent)(nil)
	_ core.AgentDoctorInfo    = (*Agent)(nil)
	_ core.WorkDirSwitcher    = (*Agent)(nil)
	_ core.SessionEnvInjector = (*Agent)(nil)
)

// Agent drives Baidu Comate CLI (`comatecli run`) in headless event-stream
// mode. Each turn spawns one process; multi-turn resumes via the CLI-assigned
// conversation ID, and tool approvals are resumed with a follow-up
// `comatecli run -c <id> --approve|--reject` process.
type Agent struct {
	workDir                string
	cmd                    string   // CLI binary name (default: "comatecli")
	cliExtraArgs           []string // extra args from cmd after the binary name
	configEnv              []string // env vars from [projects.agent.options.env]
	sessionEnv             []string // env vars injected per session via SetSessionEnv
	model                  string
	mode                   string // "" | agent | ask | plan | cloud | bot
	autoApprove            bool   // true: rely on comatecli built-in auto-approval
	disableHooks           bool   // true: pass --disable-hooks to comatecli
	enableInteractiveTools bool   // true: pass --enable-interactive-tools to comatecli
	mu                     sync.Mutex
}

// New builds an Agent from [projects.agent.options] entries:
//
//	work_dir                  working directory for comatecli (default ".")
//	model                     model name passed as --model
//	mode                      Agent | Ask | Plan | Cloud | Bot (case-insensitive)
//	auto_approve              skip the IM approval loop (default false)
//	disable_hooks             pass --disable-hooks
//	enable_interactive_tools  pass --enable-interactive-tools
//	cmd                       binary name or full command line (default "comatecli")
//	env                       static env vars merged into every session
func New(opts map[string]any) (core.Agent, error) {
	workDir, _ := opts["work_dir"].(string)
	if workDir == "" {
		workDir = "."
	}
	model, _ := opts["model"].(string)
	mode := normalizeMode(opts["mode"])
	autoApprove, _ := opts["auto_approve"].(bool)
	disableHooks, _ := opts["disable_hooks"].(bool)
	enableInteractiveTools, _ := opts["enable_interactive_tools"].(bool)

	cmd, extraArgs := core.ParseCmdOpts(opts, "comatecli")
	if _, err := exec.LookPath(cmd); err != nil {
		return nil, fmt.Errorf("comate: %q not found in PATH, install with: npm install -g comatecli", cmd)
	}

	return &Agent{
		workDir:                workDir,
		cmd:                    cmd,
		cliExtraArgs:           extraArgs,
		configEnv:              core.ParseConfigEnv(opts),
		model:                  model,
		mode:                   mode,
		autoApprove:            autoApprove,
		disableHooks:           disableHooks,
		enableInteractiveTools: enableInteractiveTools,
	}, nil
}

// normalizeMode validates the comatecli work mode. Empty keeps the CLI
// default (Agent).
func normalizeMode(raw any) string {
	s, ok := raw.(string)
	if !ok {
		return ""
	}
	switch mode := strings.ToLower(strings.TrimSpace(s)); mode {
	case "agent", "ask", "plan", "cloud", "bot":
		return mode
	case "":
		return ""
	default:
		slog.Warn("comate: unknown mode, using CLI default", "mode", s)
		return ""
	}
}

// Name implements core.Agent.
func (a *Agent) Name() string           { return "comate" }
func (a *Agent) CLIBinaryName() string  { return a.cmd }
func (a *Agent) CLIDisplayName() string { return "Comate" }

// SetWorkDir implements core.WorkDirSwitcher (/dir command). Applies to
// sessions started afterwards; running sessions keep their own directory.
func (a *Agent) SetWorkDir(dir string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.workDir = dir
	slog.Info("comate: work_dir changed", "work_dir", dir)
}

// GetWorkDir implements core.WorkDirSwitcher.
func (a *Agent) GetWorkDir() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.workDir
}

// SetSessionEnv implements core.SessionEnvInjector. Platform-injected env
// (e.g. IM user identity) is layered on top of the static config env.
func (a *Agent) SetSessionEnv(env []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessionEnv = env
}

// StartSession creates a session object keyed by the CLI conversation ID.
// The process itself is only spawned on the first Send — one process per
// turn. A non-empty sessionID resumes that conversation via `-c <id>`;
// core.ContinueSession is treated as a fresh conversation (see session.go).
func (a *Agent) StartSession(ctx context.Context, sessionID string) (core.AgentSession, error) {
	a.mu.Lock()
	cmd := a.cmd
	extraArgs := append([]string{}, a.cliExtraArgs...)
	workDir := a.workDir
	model := a.model
	mode := a.mode
	autoApprove := a.autoApprove
	disableHooks := a.disableHooks
	enableInteractiveTools := a.enableInteractiveTools
	extraEnv := append([]string(nil), a.configEnv...)
	extraEnv = append(extraEnv, a.sessionEnv...)
	a.mu.Unlock()

	return newComateSession(ctx, cmd, extraArgs, workDir, model, mode,
		autoApprove, disableHooks, enableInteractiveTools, sessionID, extraEnv), nil
}

// ListSessions implements core.Agent. comatecli has no local session-list
// API, so IM-side /sessions shows nothing for this agent type.
func (a *Agent) ListSessions(_ context.Context) ([]core.AgentSessionInfo, error) {
	return nil, nil
}

// Stop implements core.Agent; sessions own their processes and close
// themselves, so there is nothing agent-level to tear down.
func (a *Agent) Stop() error { return nil }
