package grok

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewUsesSafeDefaultsAndInheritsLocalModel(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	workDir := t.TempDir()

	raw, err := New(map[string]any{
		"work_dir": workDir,
		"cmd":      executable,
	})
	require.NoError(t, err)

	agent := raw.(*Agent)
	wantWorkDir, err := filepath.Abs(workDir)
	require.NoError(t, err)
	assert.Equal(t, "grok", agent.Name())
	assert.Equal(t, executable, agent.CLIBinaryName())
	assert.Equal(t, "Grok Build", agent.CLIDisplayName())
	assert.Equal(t, wantWorkDir, agent.GetWorkDir())
	assert.Empty(t, agent.GetModel(), "an empty model must inherit the local Grok default")
	assert.Equal(t, "default", agent.GetMode(), "unattended approval must require explicit yolo opt-in")
	assert.Zero(t, agent.timeout)
	assert.Zero(t, agent.maxTurns)
}

func TestNewAppliesExplicitOptions(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)

	raw, err := New(map[string]any{
		"work_dir":         t.TempDir(),
		"cmd":              executable + " helper-subcommand",
		"model":            " grok-test ",
		"mode":             "acceptEdits",
		"reasoning_effort": " high ",
		"timeout_mins":     float64(2),
		"max_turns":        int64(7),
		"env": map[string]any{
			"GROK_HOME": "/tmp/grok-test-home",
		},
	})
	require.NoError(t, err)

	agent := raw.(*Agent)
	assert.Equal(t, executable, agent.cmd)
	assert.Equal(t, []string{"helper-subcommand"}, agent.cliExtraArgs)
	assert.Equal(t, "grok-test", agent.GetModel())
	assert.Equal(t, "accept_edits", agent.GetMode())
	assert.Equal(t, "high", agent.GetReasoningEffort())
	assert.Equal(t, 2*time.Minute, agent.timeout)
	assert.Equal(t, 7, agent.maxTurns)
	assert.Contains(t, agent.configEnv, "GROK_HOME=/tmp/grok-test-home")
}

func TestNewValidatesWorkDirAndBinary(t *testing.T) {
	t.Run("missing work directory", func(t *testing.T) {
		_, err := New(map[string]any{
			"work_dir": filepath.Join(t.TempDir(), "missing"),
			"cmd":      os.Args[0],
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "work_dir")
	})

	t.Run("work directory is a file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "file")
		require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))
		_, err := New(map[string]any{"work_dir": path, "cmd": os.Args[0]})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a directory")
	})

	t.Run("missing binary", func(t *testing.T) {
		_, err := New(map[string]any{
			"work_dir": t.TempDir(),
			"cmd":      "this-binary-must-not-exist-cc-connect-grok",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "CLI not found")
	})
}

func TestNormalizeMode(t *testing.T) {
	tests := map[string]string{
		"":                  "default",
		"default":           "default",
		"ask":               "default",
		"yolo":              "yolo",
		"YOLO":              "yolo",
		"bypassPermissions": "yolo",
		"plan":              "plan",
		"acceptEdits":       "accept_edits",
		"auto_edit":         "accept_edits",
		"auto":              "auto",
		"dontAsk":           "dont_ask",
		"dont_ask":          "dont_ask",
		"unknown":           "default",
	}
	for input, want := range tests {
		t.Run(input, func(t *testing.T) {
			assert.Equal(t, want, normalizeMode(input))
		})
	}
}

func TestPermissionModeFlag(t *testing.T) {
	tests := map[string]string{
		"default":      "default",
		"yolo":         "bypassPermissions",
		"plan":         "plan",
		"auto":         "auto",
		"accept_edits": "acceptEdits",
		"dont_ask":     "dontAsk",
	}
	for mode, want := range tests {
		t.Run(mode, func(t *testing.T) {
			assert.Equal(t, want, permissionModeFlag(mode))
		})
	}
}

func TestParseModelsOutput(t *testing.T) {
	models := parseModelsOutput("" +
		"Available models:\n" +
		"* grok-4.5 (recommended)\n" +
		"  grok-4-fast\n" +
		"  not-a-grok-model\n" +
		"* grok-4.5 duplicate\n")
	require.Len(t, models, 2)
	assert.Equal(t, "grok-4-fast", models[0].Name)
	assert.Equal(t, "grok-4.5", models[1].Name)
}

func TestProviderEnvironmentUsesCurrentGrokVariableNames(t *testing.T) {
	agent := &Agent{activeIdx: -1}
	agent.SetProviders([]core.ProviderConfig{{
		Name:    "xai",
		APIKey:  "test-key",
		BaseURL: "https://example.invalid/v1",
		Env:     map[string]string{"EXTRA": "1"},
	}})
	require.True(t, agent.SetActiveProvider("xai"))

	env := agent.providerEnvLocked()
	assert.Contains(t, env, "XAI_API_KEY=test-key")
	assert.Contains(t, env, "GROK_MODELS_BASE_URL=https://example.invalid/v1")
	assert.Contains(t, env, "EXTRA=1")
	assert.NotContains(t, env, "XAI_API_BASE_URL=https://example.invalid/v1")
}
