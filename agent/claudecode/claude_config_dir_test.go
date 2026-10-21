package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// TestValidateSessionID_HonoursClaudeConfigDir pins that transcript lookup
// follows CLAUDE_CONFIG_DIR the same way the claude subprocess does: the
// env cc-connect passes to claude first, then the process env, then
// $HOME/.claude.
func TestValidateSessionID_HonoursClaudeConfigDir(t *testing.T) {
	const sessionID = "abc123-def456"

	tests := []struct {
		name        string
		processDir  string // CLAUDE_CONFIG_DIR in the cc-connect process env ("" = unset)
		configEnv   []string
		providerEnv map[string]string
		sessionEnv  []string
		transcript  string // config dir (relative to HOME) the transcript is written under
		want        bool
	}{
		{
			name:       "process env, transcript under custom dir",
			processDir: "custom",
			transcript: "custom",
			want:       true,
		},
		{
			name:       "process env, transcript only under default dir",
			processDir: "custom",
			transcript: ".claude",
			want:       false,
		},
		{
			name:       "configured env only",
			configEnv:  []string{"CLAUDE_CONFIG_DIR=custom"},
			transcript: "custom",
			want:       true,
		},
		{
			name:        "provider env only",
			providerEnv: map[string]string{"CLAUDE_CONFIG_DIR": "custom"},
			transcript:  "custom",
			want:        true,
		},
		{
			name:       "configured env overrides process env",
			processDir: "from-process",
			configEnv:  []string{"CLAUDE_CONFIG_DIR=custom"},
			transcript: "custom",
			want:       true,
		},
		{
			name:       "session env overrides configured env",
			configEnv:  []string{"CLAUDE_CONFIG_DIR=from-config"},
			sessionEnv: []string{"CLAUDE_CONFIG_DIR=custom"},
			transcript: "custom",
			want:       true,
		},
		{
			name:       "unset falls back to HOME/.claude",
			transcript: ".claude",
			want:       true,
		},
		{
			name:       "tilde expanded",
			configEnv:  []string{"CLAUDE_CONFIG_DIR=~/custom"},
			transcript: "custom",
			want:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			abs := func(env []string) []string {
				out := make([]string, len(env))
				for i, kv := range env {
					k, v, _ := strings.Cut(kv, "=")
					if v != "" && v[0] != '~' {
						v = filepath.Join(home, v)
					}
					out[i] = k + "=" + v
				}
				return out
			}
			if tt.processDir != "" {
				t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, tt.processDir))
			} else {
				t.Setenv("CLAUDE_CONFIG_DIR", "")
			}

			workDir := filepath.Join(home, "work", "proj")
			projectDir := filepath.Join(home, tt.transcript, "projects", encodeClaudeProjectKey(workDir))
			if err := os.MkdirAll(projectDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(projectDir, sessionID+".jsonl"), []byte("{}"), 0o644); err != nil {
				t.Fatal(err)
			}

			a := &Agent{
				workDir:    workDir,
				configEnv:  abs(tt.configEnv),
				sessionEnv: abs(tt.sessionEnv),
				activeIdx:  -1,
			}
			if tt.providerEnv != nil {
				env := map[string]string{}
				for k, v := range tt.providerEnv {
					env[k] = filepath.Join(home, v)
				}
				a.providers = []core.ProviderConfig{{Name: "p", Env: env}}
				a.activeIdx = 0
			}

			if got := a.ValidateSessionID(context.Background(), sessionID); got != tt.want {
				t.Errorf("ValidateSessionID = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestGlobalFilesHonourClaudeConfigDir pins that the global CLAUDE.md and
// the user commands dir live under the effective config dir.
func TestGlobalFilesHonourClaudeConfigDir(t *testing.T) {
	tests := []struct {
		name       string
		processDir string
		configEnv  []string
		wantDir    string // relative to HOME
	}{
		{name: "unset", wantDir: ".claude"},
		{name: "process env", processDir: "proc", wantDir: "proc"},
		{name: "configured env", processDir: "proc", configEnv: []string{"CLAUDE_CONFIG_DIR=~/cfg"}, wantDir: "cfg"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			if tt.processDir != "" {
				t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, tt.processDir))
			} else {
				t.Setenv("CLAUDE_CONFIG_DIR", "")
			}
			a := &Agent{workDir: filepath.Join(home, "work"), configEnv: tt.configEnv, activeIdx: -1}

			if got, want := a.GlobalMemoryFile(), filepath.Join(home, tt.wantDir, "CLAUDE.md"); got != want {
				t.Errorf("GlobalMemoryFile = %q, want %q", got, want)
			}
			dirs := a.CommandDirs()
			if got, want := dirs[len(dirs)-1], filepath.Join(home, tt.wantDir, "commands"); got != want {
				t.Errorf("user commands dir = %q, want %q", got, want)
			}
		})
	}
}
