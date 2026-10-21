package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// stubConfigDirAgent reports a config dir the way claudecode.Agent does when
// CLAUDE_CONFIG_DIR comes from its configured env.
type stubConfigDirAgent struct {
	core.Agent
	dir string
}

func (a stubConfigDirAgent) ClaudeConfigDir() string { return a.dir }

func TestResolveClaudeProjectDir_HonoursClaudeConfigDir(t *testing.T) {
	tests := []struct {
		name       string
		processDir string // CLAUDE_CONFIG_DIR ("" = unset), relative to HOME unless it starts with ~
		agentDir   string // dir reported by the agent's ClaudeConfigDir, relative to HOME ("" = not implemented)
		configDir  string // config dir (relative to HOME) the project dir is created under
		want       bool
	}{
		{name: "unset uses HOME/.claude", configDir: ".claude", want: true},
		{name: "process env", processDir: "custom", configDir: "custom", want: true},
		{name: "process env ignores HOME/.claude", processDir: "custom", configDir: ".claude", want: false},
		{name: "tilde expanded", processDir: "~/custom", configDir: "custom", want: true},
		{name: "agent config dir overrides process env", processDir: "proc", agentDir: "custom", configDir: "custom", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			switch {
			case tt.processDir == "":
				t.Setenv("CLAUDE_CONFIG_DIR", "")
			case strings.HasPrefix(tt.processDir, "~"):
				t.Setenv("CLAUDE_CONFIG_DIR", tt.processDir)
			default:
				t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, tt.processDir))
			}
			workDir := filepath.Join(home, "work")
			dir := filepath.Join(home, tt.configDir, "projects", strings.ReplaceAll(workDir, string(os.PathSeparator), "-"))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			want := ""
			if tt.want {
				want = dir
			}
			var agent core.Agent
			if tt.agentDir != "" {
				agent = stubConfigDirAgent{dir: filepath.Join(home, tt.agentDir)}
			}
			if got := resolveClaudeProjectDir(claudeConfigDir(agent), workDir); got != want {
				t.Errorf("resolveClaudeProjectDir = %q, want %q", got, want)
			}
		})
	}
}
