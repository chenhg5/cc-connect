package kimi

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func skipUnlessKimiAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("kimi"); err != nil {
		t.Skipf("kimi CLI not in PATH, skipping: %v", err)
	}
}

func TestNormalizeMode(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"default", "default"},
		{"DEFAULT", "default"},
		{"yolo", "yolo"},
		{"YOLO", "yolo"},
		{"force", "yolo"},
		{"bypass", "yolo"},
		{"auto", "yolo"},
		{"plan", "plan"},
		{"quiet", "quiet"},
		{"", "default"},
		{"unknown", "default"},
	}

	for _, c := range cases {
		assert.Equal(t, c.expected, normalizeMode(c.input), "input: %s", c.input)
	}
}

func TestAgentNew(t *testing.T) {
	skipUnlessKimiAvailable(t)
	agentInf, err := New(map[string]any{
		"work_dir":     "/tmp",
		"model":        "kimi-k2",
		"mode":         "yolo",
		"timeout_mins": 15,
	})
	require.NoError(t, err)
	require.NotNil(t, agentInf)

	a := agentInf.(*Agent)
	assert.Equal(t, "kimi", a.Name())
	assert.Equal(t, "/tmp", a.GetWorkDir())
	assert.Equal(t, "yolo", a.GetMode())
	assert.Equal(t, "kimi-k2", a.GetModel())
}

// TestAgentFields verifies Name/WorkDir/Mode/Model without requiring
// the kimi CLI on PATH — constructs the struct directly.
func TestAgentFields(t *testing.T) {
	a := &Agent{
		workDir:   "/tmp",
		model:     "kimi-k2",
		mode:      "yolo",
		cmd:       "kimi",
		activeIdx: -1,
	}
	assert.Equal(t, "kimi", a.Name())
	assert.Equal(t, "Kimi", a.CLIDisplayName())
	assert.Equal(t, "kimi", a.CLIBinaryName())
	assert.Equal(t, "/tmp", a.GetWorkDir())
	assert.Equal(t, "yolo", a.GetMode())
	assert.Equal(t, "kimi-k2", a.GetModel())
}

func TestAgentSetters(t *testing.T) {
	a := &Agent{workDir: "/tmp", mode: "default", activeIdx: -1}

	a.SetWorkDir("/new/path")
	assert.Equal(t, "/new/path", a.GetWorkDir())

	a.SetModel("kimi-k2-5")
	assert.Equal(t, "kimi-k2-5", a.GetModel())

	a.SetMode("plan")
	assert.Equal(t, "plan", a.GetMode())
}

func TestAgentPermissionModes(t *testing.T) {
	a := &Agent{}

	modes := a.PermissionModes()
	require.Len(t, modes, 4)
	assert.Equal(t, "default", modes[0].Key)
	assert.Equal(t, "yolo", modes[1].Key)
	assert.Equal(t, "plan", modes[2].Key)
	assert.Equal(t, "quiet", modes[3].Key)
}

func TestAgentProviderSwitcher(t *testing.T) {
	a := &Agent{workDir: "/tmp", activeIdx: -1}

	providers := []core.ProviderConfig{
		{Name: "moonshot", APIKey: "sk-123"},
		{Name: "custom", BaseURL: "https://api.example.com"},
	}
	a.SetProviders(providers)

	assert.False(t, a.SetActiveProvider("missing"))
	assert.True(t, a.SetActiveProvider("moonshot"))
	assert.Equal(t, "moonshot", a.GetActiveProvider().Name)

	list := a.ListProviders()
	require.Len(t, list, 2)
	assert.Equal(t, "moonshot", list[0].Name)
}

func TestAgentStartSession(t *testing.T) {
	skipUnlessKimiAvailable(t)
	agentInf, err := New(map[string]any{
		"work_dir":     "/tmp",
		"model":        "kimi-k2",
		"mode":         "default",
		"timeout_mins": 10,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	session, err := agentInf.StartSession(ctx, "test-session-id")
	require.NoError(t, err)
	require.NotNil(t, session)
	assert.True(t, session.Alive())
	assert.Equal(t, "test-session-id", session.CurrentSessionID())

	err = session.Close()
	assert.NoError(t, err)
	assert.False(t, session.Alive())
}

func TestAgentMemoryAndSkill(t *testing.T) {
	a := &Agent{workDir: "/tmp/my-project", activeIdx: -1}

	assert.Equal(t, "/tmp/my-project/AGENTS.md", a.ProjectMemoryFile())
	assert.NotEmpty(t, a.GlobalMemoryFile())

	skillDirs := a.SkillDirs()
	require.Len(t, skillDirs, 2)
	assert.Contains(t, skillDirs[0], ".kimi/skills")
	assert.Contains(t, skillDirs[1], ".kimi/skills")
}

func TestAgentAvailableModels(t *testing.T) {
	a := &Agent{workDir: "/tmp", activeIdx: -1}

	models := a.AvailableModels(context.Background())
	require.True(t, len(models) > 0)
}
