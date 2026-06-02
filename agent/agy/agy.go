package agy

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterAgent("agy", New)
}

// Agent drives the Antigravity CLI (agy) in headless mode using -p (--print).
// Each Send() spawns a new subprocess. Session continuity via --conversation.
type Agent struct {
	workDir        string
	cmd            string
	timeout        time.Duration
	sessionEnv     []string
	platformPrompt string
	mu             sync.RWMutex
}

func New(opts map[string]any) (core.Agent, error) {
	workDir, _ := opts["work_dir"].(string)
	if workDir == "" {
		workDir = "."
	}
	cmd, _ := opts["cmd"].(string)
	if cmd == "" {
		cmd = "agy"
	}

	var timeoutMins int64
	switch v := opts["timeout_mins"].(type) {
	case int64:
		timeoutMins = v
	case int:
		timeoutMins = int64(v)
	case float64:
		timeoutMins = int64(v)
	default:
		if v != nil {
			slog.Debug("agy: timeout_mins has unexpected type", "type", fmt.Sprintf("%T", v))
		}
	}
	var timeout time.Duration
	if timeoutMins > 0 {
		timeout = time.Duration(timeoutMins) * time.Minute
	}

	if _, err := exec.LookPath(cmd); err != nil {
		return nil, fmt.Errorf("agy: %q not found in PATH; ensure agy is installed", cmd)
	}

	return &Agent{workDir: workDir, cmd: cmd, timeout: timeout}, nil
}

// NormalizeModeExported is exported only for unit tests.
func NormalizeModeExported(raw string) string { return normalizeMode(raw) }

func normalizeMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "yolo", "auto", "force":
		return "yolo"
	default:
		return "default"
	}
}

func (a *Agent) Name() string           { return "agy" }
func (a *Agent) CLIBinaryName() string  { return a.cmd }
func (a *Agent) CLIDisplayName() string { return "Agy" }
func (a *Agent) HasSystemPromptSupport() bool {
	return true
}

func (a *Agent) SetWorkDir(dir string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.workDir = dir
	slog.Info("agy: work_dir changed", "work_dir", dir)
}

func (a *Agent) GetWorkDir() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.workDir
}

func (a *Agent) SetSessionEnv(env []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessionEnv = env
}

func (a *Agent) SetPlatformPrompt(prompt string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.platformPrompt = prompt
}

func (a *Agent) StartSession(ctx context.Context, sessionID string) (core.AgentSession, error) {
	a.mu.RLock()
	cmd, workDir, timeout := a.cmd, a.workDir, a.timeout
	extraEnv := append([]string(nil), a.sessionEnv...)
	platformPrompt := a.platformPrompt
	a.mu.RUnlock()
	return newAgySessionWithOptions(ctx, cmd, workDir, sessionID, extraEnv, core.AgentSystemPrompt(), platformPrompt, timeout)
}

// ListSessions returns nil — agy stores conversations as protobuf blobs
// (~/.gemini/antigravity-cli/conversations/*.pb) without a per-workdir index.
func (a *Agent) ListSessions(_ context.Context) ([]core.AgentSessionInfo, error) {
	return nil, nil
}

func (a *Agent) DeleteSession(_ context.Context, _ string) error {
	return fmt.Errorf("agy: session deletion is not supported")
}

func (a *Agent) Stop() error { return nil }

func (a *Agent) ProjectMemoryFile() string {
	a.mu.RLock()
	workDir := a.workDir
	a.mu.RUnlock()
	absDir, err := filepath.Abs(workDir)
	if err != nil {
		absDir = workDir
	}
	return filepath.Join(absDir, "AGENTS.md")
}

func (a *Agent) GlobalMemoryFile() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(homeDir, ".gemini", "antigravity-cli", "AGENTS.md")
}
