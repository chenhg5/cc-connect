package acp

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// defaultAuthTimeout is the maximum time to wait for the authenticate RPC to
// complete. Device-login flows (e.g. cursor_login) block until the user
// approves in a browser; without a timeout the session hangs indefinitely.
const defaultAuthTimeout = 2 * time.Minute

func init() {
	core.RegisterAgent("acp", New)
}

// Agent runs an ACP (Agent Client Protocol) agent subprocess over stdio JSON-RPC.
type Agent struct {
	workDir     string
	command     string
	args        []string
	extraEnv    []string
	sessionEnv  []string
	authMethod  string        // optional, e.g. "cursor_login" for Cursor CLI (see authenticate RPC)
	authTimeout time.Duration // max wait for authenticate RPC; 0 = no extra timeout (use session ctx)
	displayName string        // optional, for doctor (default "ACP")
	mu          sync.RWMutex
}

// New builds an acp agent from project options.
// Required: options["command"] — executable name or path for the ACP agent.
// Optional: options["args"], options["env"], options["auth_method"], options["auth_timeout"], options["display_name"].
func New(opts map[string]any) (core.Agent, error) {
	workDir, _ := opts["work_dir"].(string)
	if workDir == "" {
		workDir = "."
	}
	cmdStr, _ := opts["command"].(string)
	cmdStr = strings.TrimSpace(cmdStr)
	if cmdStr == "" {
		return nil, fmt.Errorf("acp: agent option \"command\" is required (path or name of the ACP agent binary)")
	}
	if _, err := exec.LookPath(cmdStr); err != nil {
		return nil, fmt.Errorf("acp: command %q not found in PATH: %w", cmdStr, err)
	}

	args := parseStringSlice(opts["args"])
	extra := envPairsFromOpts(opts)
	authMethod, _ := opts["auth_method"].(string)
	authMethod = strings.TrimSpace(authMethod)
	authTimeout := defaultAuthTimeout
	if v, ok := opts["auth_timeout"].(string); ok && v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("acp: invalid auth_timeout %q: %w", v, err)
		}
		if d < 0 {
			return nil, fmt.Errorf("acp: auth_timeout must not be negative: %s", v)
		}
		authTimeout = d // 0 = no extra timeout (use session context)
	}
	displayName, _ := opts["display_name"].(string)
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		displayName = "ACP"
	}

	return &Agent{
		workDir:     workDir,
		command:     cmdStr,
		args:        args,
		extraEnv:    extra,
		authMethod:  authMethod,
		authTimeout: authTimeout,
		displayName: displayName,
	}, nil
}

func envPairsFromOpts(opts map[string]any) []string {
	raw, ok := opts["env"]
	if !ok || raw == nil {
		return nil
	}
	switch m := raw.(type) {
	case map[string]string:
		var out []string
		for k, v := range m {
			out = append(out, k+"="+v)
		}
		return out
	case map[string]any:
		var out []string
		for k, v := range m {
			out = append(out, fmt.Sprintf("%s=%v", k, v))
		}
		return out
	default:
		return nil
	}
}

func parseStringSlice(v any) []string {
	switch x := v.(type) {
	case nil:
		return nil
	case []string:
		return append([]string(nil), x...)
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			switch t := e.(type) {
			case string:
				out = append(out, t)
			default:
				out = append(out, fmt.Sprint(t))
			}
		}
		return out
	default:
		return nil
	}
}

func (a *Agent) Name() string { return "acp" }

func (a *Agent) SetWorkDir(dir string) {
	a.mu.Lock()
	a.workDir = dir
	a.mu.Unlock()
	slog.Info("acp: work_dir changed", "work_dir", dir)
}

func (a *Agent) GetWorkDir() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.workDir
}

func (a *Agent) SetSessionEnv(env []string) {
	a.mu.Lock()
	a.sessionEnv = env
	a.mu.Unlock()
}

func (a *Agent) StartSession(ctx context.Context, sessionID string) (core.AgentSession, error) {
	a.mu.RLock()
	command := a.command
	args := a.args
	workDir := a.workDir
	authMethod := a.authMethod
	authTimeout := a.authTimeout
	extra := append([]string(nil), a.extraEnv...)
	extra = append(extra, a.sessionEnv...)
	a.mu.RUnlock()

	return newACPSession(ctx, command, args, extra, workDir, sessionID, authMethod, authTimeout)
}

func (a *Agent) ListSessions(context.Context) ([]core.AgentSessionInfo, error) {
	// MVP: session/list requires capability negotiation per ACP; omitted until needed.
	return nil, nil
}

func (a *Agent) Stop() error { return nil }

// -- AgentDoctorInfo --

func (a *Agent) CLIBinaryName() string {
	a.mu.RLock()
	cmd := a.command
	a.mu.RUnlock()
	return filepath.Base(cmd)
}

func (a *Agent) CLIDisplayName() string {
	a.mu.RLock()
	n := a.displayName
	a.mu.RUnlock()
	if n == "" {
		return "ACP"
	}
	return n
}
