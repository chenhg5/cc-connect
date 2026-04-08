package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

type stubMainAgent struct {
	workDir string
}

func (a *stubMainAgent) Name() string { return "stub-main" }

func (a *stubMainAgent) StartSession(_ context.Context, _ string) (core.AgentSession, error) {
	return &stubMainAgentSession{}, nil
}

func (a *stubMainAgent) ListSessions(_ context.Context) ([]core.AgentSessionInfo, error) {
	return nil, nil
}

func (a *stubMainAgent) Stop() error { return nil }

func (a *stubMainAgent) SetWorkDir(dir string) {
	a.workDir = dir
}

func (a *stubMainAgent) GetWorkDir() string {
	return a.workDir
}

type stubMainAgentSession struct{}

func (s *stubMainAgentSession) Send(string, []core.ImageAttachment, []core.FileAttachment) error {
	return nil
}
func (s *stubMainAgentSession) RespondPermission(string, core.PermissionResult) error { return nil }
func (s *stubMainAgentSession) Events() <-chan core.Event                             { return nil }
func (s *stubMainAgentSession) Close() error                                          { return nil }
func (s *stubMainAgentSession) CurrentSessionID() string                              { return "" }
func (s *stubMainAgentSession) Alive() bool                                           { return true }

func TestProjectStatePath(t *testing.T) {
	dataDir := t.TempDir()
	got := projectStatePath(dataDir, "my/project:one")
	want := filepath.Join(dataDir, "projects", "my_project_one.state.json")
	if got != want {
		t.Fatalf("projectStatePath() = %q, want %q", got, want)
	}
}

func TestApplyProjectStateOverride(t *testing.T) {
	baseDir := t.TempDir()
	overrideDir := filepath.Join(t.TempDir(), "override")
	if err := os.Mkdir(overrideDir, 0o755); err != nil {
		t.Fatalf("mkdir override dir: %v", err)
	}

	store := core.NewProjectStateStore(filepath.Join(t.TempDir(), "projects", "demo.state.json"))
	store.SetWorkDirOverride(overrideDir)

	agent := &stubMainAgent{workDir: baseDir}
	got := applyProjectStateOverride("demo", agent, baseDir, store)

	if got != overrideDir {
		t.Fatalf("applyProjectStateOverride() = %q, want %q", got, overrideDir)
	}
	if agent.workDir != overrideDir {
		t.Fatalf("agent workDir = %q, want %q", agent.workDir, overrideDir)
	}
}
