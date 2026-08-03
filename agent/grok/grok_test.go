package grok

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

func TestGlobalMemoryFileReturnsTargetBeforeItExists(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	grokHome := filepath.Join(t.TempDir(), "new-grok-home")

	raw, err := New(map[string]any{
		"work_dir": t.TempDir(),
		"cmd":      executable,
		"env": map[string]any{
			"GROK_HOME": grokHome,
		},
	})
	require.NoError(t, err)
	require.NoDirExists(t, grokHome)

	assert.Equal(t, filepath.Join(grokHome, "AGENTS.md"), raw.(*Agent).GlobalMemoryFile())
	assert.NoDirExists(t, grokHome, "reading the memory target must not create it")
}

func TestAgentStorePathsUseEffectiveEnvironment(t *testing.T) {
	processHome := t.TempDir()
	optionsHome := filepath.Join(t.TempDir(), "options-home")
	workDir := filepath.Join(t.TempDir(), "workspace")
	grokHome := filepath.Join(optionsHome, ".grok")
	require.NoError(t, os.MkdirAll(workDir, 0o755))
	t.Setenv("HOME", processHome)
	t.Setenv("GROK_HOME", filepath.Join(processHome, "inherited-grok-home"))

	writeGrokSummaryFixture(t, grokHome, "workspace", "effective-session", workDir, "effective", time.Now().Format(time.RFC3339Nano))
	writeGrokSummaryFixture(t, filepath.Join(processHome, "inherited-grok-home"), "workspace", "wrong-session", workDir, "wrong", time.Now().Format(time.RFC3339Nano))

	agent := &Agent{
		workDir: workDir,
		configEnv: []string{
			"HOME=" + optionsHome,
			"GROK_HOME=",
		},
		activeIdx: -1,
	}

	sessions, err := agent.ListSessions(context.Background())
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	assert.Equal(t, "effective-session", sessions[0].ID)
	assert.True(t, agent.ValidateSessionID(context.Background(), "effective-session"))
	assert.False(t, agent.ValidateSessionID(context.Background(), "wrong-session"))
	assert.Equal(t, filepath.Join(grokHome, "AGENTS.md"), agent.GlobalMemoryFile())
}

func TestGrokLayeredEnvironmentUsesOneLastWinsValue(t *testing.T) {
	workDir := t.TempDir()
	configHome := filepath.Join(t.TempDir(), "config-home")
	providerHome := filepath.Join(t.TempDir(), "provider-home")
	sessionHome := filepath.Join(t.TempDir(), "session-home")
	writeGrokSummaryFixture(t, sessionHome, "workspace", "layered-session", workDir, "layered", time.Now().Format(time.RFC3339Nano))
	t.Setenv("GROK_HOME", filepath.Join(t.TempDir(), "inherited-home"))

	agent := &Agent{
		workDir: workDir,
		configEnv: []string{
			"GROK_HOME=" + configHome,
			"XAI_API_KEY=config-key",
		},
		providers: []core.ProviderConfig{{
			Name:   "provider",
			APIKey: "automatic-provider-key",
			Env: map[string]string{
				"GROK_HOME":   providerHome,
				"XAI_API_KEY": "explicit-provider-key",
			},
		}},
		activeIdx: 0,
		sessionEnv: []string{
			"GROK_HOME=" + sessionHome,
		},
	}

	effective := agent.effectiveEnvLocked()
	assertSingleEnvValue(t, effective, "GROK_HOME", sessionHome)
	assertSingleEnvValue(t, effective, "XAI_API_KEY", "explicit-provider-key")
	assertSingleEnvValue(t, grokProcessEnv(effective), "GROK_HOME", sessionHome)

	sessions, err := agent.ListSessions(context.Background())
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	assert.Equal(t, "layered-session", sessions[0].ID)

	session, err := agent.StartSession(context.Background(), "layered-session")
	require.NoError(t, err)
	defer func() { require.NoError(t, session.Close()) }()
	grokSession := session.(*grokSession)
	assertSingleEnvValue(t, grokSession.extraEnv, "GROK_HOME", sessionHome)
	assertSingleEnvValue(t, grokSession.processEnv, "GROK_HOME", sessionHome)
}

func TestStartSessionResolvesRelativeGrokHomeForModelCache(t *testing.T) {
	workDir := t.TempDir()
	grokHome := filepath.Join(workDir, ".state", "grok")
	require.NoError(t, os.MkdirAll(grokHome, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(grokHome, "models_cache.json"),
		[]byte(`{"models":{"relative-model":{"info":{"context_window":123456}}}}`),
		0o600,
	))

	agent := &Agent{
		workDir:   workDir,
		configEnv: []string{"GROK_HOME=.state/grok"},
		activeIdx: -1,
	}
	session, err := agent.StartSession(context.Background(), "")
	require.NoError(t, err)
	defer func() { require.NoError(t, session.Close()) }()

	grokSession := session.(*grokSession)
	assert.Equal(t, 123456, grokModelContextWindow(grokSession.processEnv, grokSession.workDir, "relative-model"))
}

func TestStartSessionValidatesResumeIDAgainstWorkspace(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "workspace-a")
	otherWorkDir := filepath.Join(t.TempDir(), "workspace-b")
	grokHome := filepath.Join(t.TempDir(), "grok-home")
	require.NoError(t, os.MkdirAll(workDir, 0o755))
	require.NoError(t, os.MkdirAll(otherWorkDir, 0o755))
	writeGrokSummaryFixture(t, grokHome, "workspace-a", "valid-session", workDir, "valid", time.Now().Format(time.RFC3339Nano))
	writeGrokSummaryFixture(t, grokHome, "workspace-b", "foreign-session", otherWorkDir, "foreign", time.Now().Format(time.RFC3339Nano))

	agent := &Agent{
		workDir:   workDir,
		configEnv: []string{"GROK_HOME=" + grokHome},
		activeIdx: -1,
	}

	valid, err := agent.StartSession(context.Background(), "valid-session")
	require.NoError(t, err)
	require.NoError(t, valid.Close())

	_, err = agent.StartSession(context.Background(), "foreign-session")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not belong to work_dir")

	_, err = agent.StartSession(context.Background(), "stale-session")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not belong to work_dir")

	continued, err := agent.StartSession(context.Background(), core.ContinueSession)
	require.NoError(t, err)
	require.NoError(t, continued.Close())
}

func TestDeleteSessionRedactsSecretsFromInheritedEnvironment(t *testing.T) {
	const sessionID = "session-delete-redaction"
	secret := "inherited-delete-secret-marker"
	workDir := t.TempDir()
	grokHome := filepath.Join(workDir, ".state", "grok")
	writeGrokSummaryFixture(t, grokHome, "workspace", sessionID, workDir, "delete redaction", time.Now().Format(time.RFC3339Nano))
	t.Setenv("GO_WANT_GROK_DELETE_HELPER", "1")
	t.Setenv("XAI_API_KEY", secret)
	t.Setenv("GROK_HOME", filepath.Join(t.TempDir(), "wrong-inherited-home"))

	agent := &Agent{
		workDir:      workDir,
		cmd:          os.Args[0],
		cliExtraArgs: []string{"-test.run=^TestGrokDeleteHelperProcess$", "--"},
		configEnv:    []string{"GROK_HOME=.state/grok"},
		activeIdx:    -1,
	}
	err := agent.DeleteSession(context.Background(), sessionID)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "missing --no-auto-update")
	assert.Contains(t, err.Error(), "XAI_API_KEY")
	assert.Contains(t, err.Error(), "[REDACTED]")
	assert.NotContains(t, err.Error(), secret)
}

func TestGrokDeleteHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_GROK_DELETE_HELPER") != "1" {
		return
	}
	if !containsArg(os.Args, "--no-auto-update") {
		_, _ = fmt.Fprintln(os.Stderr, "missing --no-auto-update")
		os.Exit(18)
	}
	_, _ = fmt.Fprintf(os.Stderr, "delete failed with XAI_API_KEY=%s\n", os.Getenv("XAI_API_KEY"))
	os.Exit(17)
}

func assertSingleEnvValue(t *testing.T, env []string, key, want string) {
	t.Helper()
	count := 0
	for _, pair := range env {
		gotKey, value, ok := strings.Cut(pair, "=")
		if ok && gotKey == key {
			count++
			assert.Equal(t, want, value)
		}
	}
	assert.Equal(t, 1, count, "%s should occur exactly once in %v", key, env)
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
		"":             "default",
		"default":      "default",
		"yolo":         "bypassPermissions",
		"plan":         "plan",
		"auto":         "auto",
		"accept_edits": "acceptEdits",
		"dont_ask":     "dontAsk",
		"unexpected":   "default",
	}
	for mode, want := range tests {
		t.Run(mode, func(t *testing.T) {
			assert.Equal(t, want, permissionModeFlag(mode))
		})
	}
}

func TestNormalizeReasoningEffortSupportsCanonicalAndModelOptionIDs(t *testing.T) {
	for _, effort := range []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"} {
		t.Run(effort, func(t *testing.T) {
			assert.Equal(t, effort, normalizeReasoningEffort(strings.ToUpper(effort)))
		})
	}
	assert.Equal(t, "deep", normalizeReasoningEffort(" deep "))
	assert.Empty(t, normalizeReasoningEffort("--always-approve"))
	assert.Empty(t, normalizeReasoningEffort("high extra"))
}

func TestAvailableReasoningEffortsUsesCurrentModelMenu(t *testing.T) {
	workDir := t.TempDir()
	grokHome := filepath.Join(t.TempDir(), ".grok")
	require.NoError(t, os.MkdirAll(grokHome, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(grokHome, "models_cache.json"), []byte(`{
		"models":{"custom-model":{"info":{"reasoning_efforts":[{"id":"deep"},{"id":"minimal"}]}}}
	}`), 0o600))
	agent := &Agent{
		workDir:   workDir,
		model:     "custom-model",
		configEnv: []string{"GROK_HOME=" + grokHome},
		activeIdx: -1,
	}
	assert.Equal(t, []string{"deep", "minimal"}, agent.AvailableReasoningEfforts())

	agent.model = "uncached-model"
	assert.Equal(t, []string{"max", "xhigh", "high", "medium", "low", "minimal", "none"},
		agent.AvailableReasoningEfforts())
}

func TestParseModelsOutput(t *testing.T) {
	models := parseModelsOutput("" +
		"Default model: ignored-before-list\n" +
		"Available models:\n" +
		"  * grok-4.5 (recommended)\n" +
		"  * vendor/custom-model (custom endpoint)\n" +
		"  not-a-list-entry\n" +
		"  * grok-4-fast\n" +
		"  * grok-4.5 duplicate\n")
	require.Len(t, models, 3)
	assert.Equal(t, "grok-4-fast", models[0].Name)
	assert.Equal(t, "grok-4.5", models[1].Name)
	assert.Equal(t, "vendor/custom-model", models[2].Name)
}

func TestAvailableModelsDisablesAutoUpdate(t *testing.T) {
	t.Setenv("GO_WANT_GROK_MODELS_HELPER", "1")
	agent := &Agent{
		workDir:      t.TempDir(),
		cmd:          os.Args[0],
		cliExtraArgs: []string{"-test.run=^TestGrokModelsHelperProcess$", "--"},
		activeIdx:    -1,
	}
	models := agent.AvailableModels(context.Background())
	require.Len(t, models, 1)
	assert.Equal(t, "helper-model", models[0].Name)
}

func TestGrokModelsHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_GROK_MODELS_HELPER") != "1" {
		return
	}
	if !containsArg(os.Args, "--no-auto-update") {
		os.Exit(18)
	}
	_, _ = fmt.Fprintln(os.Stdout, "Available models:\n  * helper-model (test)")
	os.Exit(0)
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
