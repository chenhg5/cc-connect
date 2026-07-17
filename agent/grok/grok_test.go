package grok

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeMode(t *testing.T) {
	cases := map[string]string{
		"":                  "default",
		"default":           "default",
		"yolo":              "yolo",
		"YOLO":              "yolo",
		"bypassPermissions": "yolo",
		"plan":              "plan",
		"acceptEdits":       "accept_edits",
		"auto_edit":         "accept_edits",
		"dontAsk":           "dont_ask",
		"dont_ask":          "dont_ask",
	}
	for in, want := range cases {
		assert.Equal(t, want, normalizeMode(in), "input %q", in)
	}
}

func TestPermissionModeFlag(t *testing.T) {
	assert.Equal(t, "default", permissionModeFlag("default"))
	assert.Equal(t, "bypassPermissions", permissionModeFlag("yolo"))
	assert.Equal(t, "plan", permissionModeFlag("plan"))
	assert.Equal(t, "acceptEdits", permissionModeFlag("accept_edits"))
	assert.Equal(t, "dontAsk", permissionModeFlag("dont_ask"))
}

func TestGrokSessionSlug(t *testing.T) {
	assert.Equal(t, "%2FUsers%2Fliyang", grokSessionSlug("/Users/liyang"))
	assert.Equal(t, "%2Ftmp", grokSessionSlug("/tmp"))
}

func TestParseGrokSessionDir(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "019f-test")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))

	summary := map[string]any{
		"info": map[string]any{
			"id":  "019f-test",
			"cwd": "/Users/liyang/project",
		},
		"session_summary":   "Fix the flaky test",
		"num_chat_messages": 12,
		"updated_at":        "2026-07-17T01:00:00Z",
		"last_active_at":    "2026-07-17T02:00:00Z",
	}
	data, err := json.Marshal(summary)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "summary.json"), data, 0o644))

	info := parseGrokSessionDir(sessionDir, "/Users/liyang/project")
	require.NotNil(t, info)
	assert.Equal(t, "019f-test", info.ID)
	assert.Equal(t, "Fix the flaky test", info.Summary)
	assert.Equal(t, 12, info.MessageCount)
	assert.False(t, info.ModifiedAt.IsZero())
	assert.Equal(t, 2026, info.ModifiedAt.UTC().Year())

	assert.Nil(t, parseGrokSessionDir(sessionDir, "/other/path"))
}

func TestFindGrokSessionDirEmpty(t *testing.T) {
	assert.Equal(t, "", findGrokSessionDir(""))
}

func TestAgentBasics(t *testing.T) {
	a := &Agent{
		workDir:   "/tmp/proj",
		model:     "grok-4.5",
		mode:      "default",
		cmd:       "grok",
		activeIdx: -1,
	}
	assert.Equal(t, "grok", a.Name())
	assert.Equal(t, "grok", a.CLIBinaryName())
	assert.Equal(t, "Grok Build", a.CLIDisplayName())
	assert.Equal(t, "/tmp/proj", a.GetWorkDir())
	assert.Equal(t, "grok-4.5", a.GetModel())
	assert.Equal(t, "default", a.GetMode())

	a.SetWorkDir("/tmp/other")
	assert.Equal(t, "/tmp/other", a.GetWorkDir())
	a.SetModel("grok-4.5-fast")
	assert.Equal(t, "grok-4.5-fast", a.GetModel())
	a.SetMode("yolo")
	assert.Equal(t, "yolo", a.GetMode())
	assert.NotEmpty(t, a.PermissionModes())

	models := a.AvailableModels(context.Background())
	require.NotEmpty(t, models)
	assert.Equal(t, "grok-4.5", models[0].Name)

	assert.Contains(t, a.ProjectMemoryFile(), "AGENTS.md")
	assert.Contains(t, a.SkillDirs()[0], ".grok")
}

func TestProviderAPIKeyEnv(t *testing.T) {
	a := &Agent{activeIdx: -1, cmd: "grok", workDir: "."}
	a.SetProviders([]core.ProviderConfig{
		{
			Name:    "xai",
			APIKey:  "xai-secret",
			BaseURL: "https://api.x.ai/v1",
			Env:     map[string]string{"EXTRA": "1"},
		},
	})
	assert.True(t, a.SetActiveProvider("xai"))
	env := a.providerEnvLocked()
	assert.Contains(t, env, "XAI_API_KEY=xai-secret")
	assert.Contains(t, env, "XAI_API_BASE_URL=https://api.x.ai/v1")
	assert.Contains(t, env, "EXTRA=1")

	assert.True(t, a.SetActiveProvider(""))
	assert.Nil(t, a.providerEnvLocked())
}

func TestNewRequiresBinary(t *testing.T) {
	_, err := New(map[string]any{
		"work_dir": t.TempDir(),
		"cmd":      "this-binary-should-not-exist-cc-connect-grok",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}
