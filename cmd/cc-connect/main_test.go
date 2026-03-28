package main

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

func TestValidateNoExtraTopLevelArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name: "no extra args",
			args: nil,
		},
		{
			name:    "unknown command",
			args:    []string{"bind", "--help"},
			wantErr: "unknown top-level command: bind",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateNoExtraTopLevelArgs(tt.args)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateNoExtraTopLevelArgs(%v) error = %v, want nil", tt.args, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateNoExtraTopLevelArgs(%v) error = nil, want %q", tt.args, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateNoExtraTopLevelArgs(%v) error = %q, want substring %q", tt.args, err.Error(), tt.wantErr)
			}
		})
	}
}

func TestParseRootCLIOptionsGlobalFlagsBeforeSubcommand(t *testing.T) {
	opts, err := parseRootCLIOptions([]string{"--config", "/tmp/test-config.toml", "sessions", "list"})
	if err != nil {
		t.Fatalf("parseRootCLIOptions() error = %v", err)
	}
	if opts.configPath != "/tmp/test-config.toml" {
		t.Fatalf("configPath = %q, want %q", opts.configPath, "/tmp/test-config.toml")
	}
	if opts.showVersion {
		t.Fatal("showVersion = true, want false")
	}
	wantArgs := []string{"sessions", "list"}
	if !reflect.DeepEqual(opts.args, wantArgs) {
		t.Fatalf("args = %v, want %v", opts.args, wantArgs)
	}
}

func TestParseRootCLIOptionsPreservesSubcommandHelp(t *testing.T) {
	opts, err := parseRootCLIOptions([]string{"--config", "/tmp/test-config.toml", "send", "--help"})
	if err != nil {
		t.Fatalf("parseRootCLIOptions() error = %v", err)
	}
	wantArgs := []string{"send", "--help"}
	if !reflect.DeepEqual(opts.args, wantArgs) {
		t.Fatalf("args = %v, want %v", opts.args, wantArgs)
	}
}

func TestParseRootCLIOptionsHelp(t *testing.T) {
	_, err := parseRootCLIOptions([]string{"--help"})
	if err == nil {
		t.Fatal("parseRootCLIOptions(--help) error = nil, want flag.ErrHelp")
	}
	if !strings.Contains(err.Error(), flag.ErrHelp.Error()) {
		t.Fatalf("parseRootCLIOptions(--help) error = %q, want %q", err.Error(), flag.ErrHelp.Error())
	}
}

func TestRunTopLevelCommandUnknown(t *testing.T) {
	if runTopLevelCommand([]string{"bind", "--help"}) {
		t.Fatal("runTopLevelCommand() handled unknown command")
	}
}
