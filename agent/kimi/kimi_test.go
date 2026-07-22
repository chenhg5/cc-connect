package kimi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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

// TestListKimiSessions_BothFlavors is the #1561 session-listing regression
// test: sessions created by legacy kimi-cli (~/.kimi/sessions) and by the
// Kimi Code CLI (~/.kimi-code/sessions) must both be visible, and the
// Kimi Code state.json schema ({"title","workDir"}) must be understood.
func TestListKimiSessions_BothFlavors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workDir := t.TempDir()

	// Legacy kimi-cli session.
	legacyDir := filepath.Join(home, ".kimi", "sessions", "proj-hash", "legacy-uuid-1")
	require.NoError(t, os.MkdirAll(legacyDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(legacyDir, "state.json"),
		[]byte(`{"custom_title":"legacy chat","archived":false}`), 0o644))

	// Kimi Code CLI session in the same workDir.
	modernDir := filepath.Join(home, ".kimi-code", "sessions", "wd_proj_ab12", "session_modern-1")
	require.NoError(t, os.MkdirAll(modernDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(modernDir, "state.json"),
		[]byte(`{"title":"modern chat","workDir":"`+workDir+`"}`), 0o644))

	// Kimi Code CLI session belonging to a DIFFERENT workDir — filtered out.
	otherDir := filepath.Join(home, ".kimi-code", "sessions", "wd_proj_ab12", "session_other-1")
	require.NoError(t, os.MkdirAll(otherDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(otherDir, "state.json"),
		[]byte(`{"title":"elsewhere","workDir":"/somewhere/else"}`), 0o644))

	sessions, err := listKimiSessions(workDir)
	require.NoError(t, err)

	byID := make(map[string]core.AgentSessionInfo, len(sessions))
	for _, s := range sessions {
		byID[s.ID] = s
	}

	legacy, ok := byID["legacy-uuid-1"]
	require.True(t, ok, "legacy kimi-cli session should be listed")
	assert.Equal(t, "legacy chat", legacy.Summary)

	modern, ok := byID["session_modern-1"]
	require.True(t, ok, "Kimi Code session should be listed")
	assert.Equal(t, "modern chat", modern.Summary)

	_, ok = byID["session_other-1"]
	assert.False(t, ok, "Kimi Code session from another workDir must be filtered out")

	// findKimiSessionDir must locate sessions in both roots.
	assert.NotEmpty(t, findKimiSessionDir("legacy-uuid-1"))
	assert.NotEmpty(t, findKimiSessionDir("session_modern-1"))
	assert.Empty(t, findKimiSessionDir("does-not-exist"))
}
