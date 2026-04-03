package coco

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"sync"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterAgent("coco", New)
}

// Agent drives Coco (Trae CLI)
type Agent struct {
	workDir    string
	sessionEnv []string
	mu         sync.Mutex
}

func New(opts map[string]any) (core.Agent, error) {
	workDir, _ := opts["work_dir"].(string)
	if workDir == "" {
		workDir = "."
	}

	if _, err := exec.LookPath("coco"); err != nil {
		return nil, fmt.Errorf("coco: 'coco' not found in PATH")
	}

	return &Agent{
		workDir: workDir,
	}, nil
}

func (a *Agent) Name() string           { return "coco" }
func (a *Agent) CLIBinaryName() string  { return "coco" }
func (a *Agent) CLIDisplayName() string { return "Trae CLI (Coco)" }

func (a *Agent) SetWorkDir(dir string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.workDir = dir
	slog.Info("coco: work_dir changed", "work_dir", dir)
}

func (a *Agent) GetWorkDir() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.workDir
}

func (a *Agent) SetSessionEnv(env []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessionEnv = env
}

func (a *Agent) StartSession(ctx context.Context, sessionID string) (core.AgentSession, error) {
	a.mu.Lock()
	extraEnv := append([]string{}, a.sessionEnv...)
	a.mu.Unlock()

	return newCocoSession(ctx, a.workDir, sessionID, extraEnv)
}

func (a *Agent) ListSessions(_ context.Context) ([]core.AgentSessionInfo, error) {
	// 暂不支持列出历史会话
	return nil, nil
}

func (a *Agent) Stop() error { return nil }
