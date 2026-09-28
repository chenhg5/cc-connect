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

func TestAgentSetModel_UpdatesActiveProviderForNextSession(t *testing.T) {
	a := &Agent{
		workDir: "/tmp",
		providers: []core.ProviderConfig{{
			Name:  "custom",
			Model: "provider/old-model",
		}},
		activeIdx: 0,
	}

	a.SetModel("kimi-cli/new-model")
	assert.Equal(t, "kimi-cli/new-model", a.GetModel())
	assert.Equal(t, "kimi-cli/new-model", a.providers[0].Model)

	session, err := a.StartSession(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, "kimi-cli/new-model", session.(*kimiSession).model)
	require.NoError(t, session.Close())
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
	a := &Agent{workDir: "/tmp", cmd: "/does/not/exist/kimi", activeIdx: -1}

	models := a.AvailableModels(context.Background())
	require.True(t, len(models) > 0)
}

// Regression: without configured models, /model used a stale hard-coded Kimi
// model list instead of the aliases configured in Kimi Code itself. Kimi Code
// exposes its catalog via `kimi provider list --json`; discovery must use the
// configured command environment.
func TestAgentAvailableModels_UsesKimiCodeCatalog(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "fake-kimi")
	script := `#!/bin/sh
if [ "$1" != "--shim" ] || [ "$2" != "provider" ] || [ "$3" != "list" ] || [ "$4" != "--json" ]; then
  exit 2
fi
if [ "$KIMI_DISCOVERY_TOKEN" != "available" ] || [ "$PWD" != "$EXPECTED_WORK_DIR" ]; then
  exit 3
fi
printf '%s\n' '{"providers":{"private":{"apiKey":"must-not-be-used"}},"models":{"private/model-z":{"displayName":"Model Z"},"private/model-a":{"displayName":"Model A"}}}'
`
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))

	workDir := t.TempDir()
	a := &Agent{
		cmd:          bin,
		cliExtraArgs: []string{"--shim"},
		workDir:      workDir,
		configEnv: []string{
			"KIMI_DISCOVERY_TOKEN=available",
			"EXPECTED_WORK_DIR=" + workDir,
		},
		activeIdx: -1,
	}

	models := a.AvailableModels(context.Background())
	require.Equal(t, []core.ModelOption{
		{Name: "private/model-a", Desc: "Model A"},
		{Name: "private/model-z", Desc: "Model Z"},
	}, models)
}

func TestAgentAvailableModels_PrefersConfiguredModels(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "fake-kimi")
	marker := filepath.Join(t.TempDir(), "cli-called")
	script := `#!/bin/sh
printf 'called' > "$KIMI_MARKER"
printf '%s\n' '{"models":{"cli/model":{}}}'
`
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))

	a := &Agent{
		cmd:       bin,
		configEnv: []string{"KIMI_MARKER=" + marker},
		providers: []core.ProviderConfig{{
			Name:   "configured",
			Models: []core.ModelOption{{Name: "configured/model"}},
		}},
		activeIdx: 0,
	}

	models := a.AvailableModels(context.Background())
	require.Equal(t, []core.ModelOption{{Name: "configured/model"}}, models)
	_, err := os.Stat(marker)
	require.ErrorIs(t, err, os.ErrNotExist, "configured models should skip CLI discovery")
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
	// Kimi Code stores the transcript at agents/main/wire.jsonl (no
	// context.jsonl). Include tool/assistant events that must NOT be counted.
	require.NoError(t, os.MkdirAll(filepath.Join(modernDir, "agents", "main"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(modernDir, "agents", "main", "wire.jsonl"), []byte(
		`{"type":"context.append_message","message":{"role":"user","content":"first ask"},"origin":{"kind":"user"}}
`+
			`{"type":"context.append_message","message":{"role":"assistant","content":"thinking..."},"origin":{"kind":"assistant"}}
`+
			`{"type":"context.append_message","message":{"role":"tool","content":"result"},"origin":{"kind":"tool_call_executor"}}
`+
			`{"type":"context.append_message","message":{"role":"user","content":"second ask"},"origin":{"kind":"user"}}
`+
			`{"type":"context.append_message","message":{"role":"user","content":"third ask"},"origin":{"kind":"user"}}
`), 0o644))

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
	// Summary now comes from the first user turn in wire.jsonl (matching the
	// legacy behavior of summarizing from the transcript), falling back to the
	// state title only when no transcript exists.
	assert.Equal(t, "first ask", modern.Summary)
	// Regression (#1564 review): Kimi Code sessions must not report 0 messages —
	// the count comes from agents/main/wire.jsonl when context.jsonl is absent.
	assert.Equal(t, 3, modern.MessageCount, "Kimi Code session should count user turns from wire.jsonl")

	_, ok = byID["session_other-1"]
	assert.False(t, ok, "Kimi Code session from another workDir must be filtered out")

	// findKimiSessionDir must locate sessions in both roots.
	assert.NotEmpty(t, findKimiSessionDir("legacy-uuid-1"))
	assert.NotEmpty(t, findKimiSessionDir("session_modern-1"))
	assert.Empty(t, findKimiSessionDir("does-not-exist"))
}

// Regression for #1564 review feedback: /list reported 0 messages for every
// Kimi Code session because the count only read context.jsonl, which the Kimi
// Code CLI does not write. This test pins the wire.jsonl fallback: user turns
// are counted, tool/assistant events are ignored, and the first user text
// becomes the summary.
func TestParseKimiSessionDir_WireJSONLMessageCount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workDir := t.TempDir()
	sessionID := "session_wire-1"
	sessionDir := filepath.Join(home, ".kimi-code", "sessions", "wd_proj_ab12", sessionID)
	require.NoError(t, os.MkdirAll(filepath.Join(sessionDir, "agents", "main"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "state.json"),
		[]byte(`{"title":"wire chat","workDir":"`+workDir+`"}`), 0o644))

	// Simulate a wire transcript: two user turns, some assistant/tool noise,
	// punctuated by a non-append event that must be skipped.
	wire := []byte(
		`{"type":"context.append_message","message":{"role":"user","content":"  hello there  "},"origin":{"kind":"user"}}
` +
			`{"type":"context.append_message","message":{"role":"assistant","content":"hi, inspect <thinking>"},"origin":{"kind":"assistant"}}
` +
			`{"type":"context.append_message","message":{"role":"tool","content":"ls output"},"origin":{"kind":"tool_call_executor"}}
` +
			`{"type":"session.started","session_id":"` + sessionID + `"}
` +
			`{"type":"context.append_message","message":{"role":"user","content":"now list sessions"},"origin":{"kind":"user"}}
`)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "agents", "main", "wire.jsonl"), wire, 0o644))

	info := parseKimiSessionDir(sessionDir, workDir)
	require.NotNil(t, info)
	assert.Equal(t, sessionID, info.ID)
	assert.Equal(t, 2, info.MessageCount,
		"only user turns should be counted from wire.jsonl")
	assert.Equal(t, "hello there", info.Summary,
		"summary should be the first user text, trimmed")
}

// Regression: Kimi sessions could be selected from /list, but /history was
// empty because the Kimi agent did not implement core.HistoryProvider. This
// fixture follows the Kimi Code 2.0 wire schema and also includes its mirrored
// agent.message.appended event, which must not duplicate assistant output.
func TestAgentGetSessionHistory_KimiCode2WireFormat(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	sessionID := "session_kimi-2-history"
	sessionDir := filepath.Join(home, ".kimi-code", "sessions", "wd_project", sessionID)
	require.NoError(t, os.MkdirAll(filepath.Join(sessionDir, "agents", "main"), 0o755))
	wire := []byte(
		`{"type":"context.append_message","time":1700000000000,"message":{"role":"user","content":[{"type":"text","text":"  first question  "}],"origin":{"kind":"user"}}}
` +
			`{"type":"context.append_loop_event","time":1700000000100,"event":{"type":"content.part","turnId":"turn-1","part":{"type":"think","think":"private reasoning"}}}
` +
			`{"type":"context.append_loop_event","time":1700000000200,"event":{"type":"content.part","turnId":"turn-1","part":{"type":"text","text":"first "}}}
` +
			`{"type":"context.append_loop_event","time":1700000000300,"event":{"type":"tool.call","turnId":"turn-1","name":"Shell"}}
` +
			`{"type":"context.append_loop_event","time":1700000000400,"event":{"type":"content.part","turnId":"turn-1","part":{"type":"text","text":"reply"}}}
` +
			`{"type":"agent.message.appended","time":1700000000401,"message":{"message":{"role":"assistant","content":[{"type":"text","text":"first reply"}]}}}
` +
			`not-json
` +
			`{"type":"context.append_message","time":1700000001000,"message":{"role":"user","content":[{"type":"text","text":"second question"}],"origin":{"kind":"user"}}}
` +
			`{"type":"context.append_loop_event","time":1700000001100,"event":{"type":"content.part","turnId":"turn-2","part":{"type":"text","text":"second reply"}}}
`)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "agents", "main", "wire.jsonl"), wire, 0o644))

	entries := getKimiSessionHistory(t, &Agent{}, sessionID, 0)
	require.Len(t, entries, 4)
	assert.Equal(t, "user", entries[0].Role)
	assert.Equal(t, "first question", entries[0].Content)
	assert.Equal(t, time.UnixMilli(1700000000000), entries[0].Timestamp)
	assert.Equal(t, "assistant", entries[1].Role)
	assert.Equal(t, "first reply", entries[1].Content)
	assert.Equal(t, time.UnixMilli(1700000000200), entries[1].Timestamp)
	assert.Equal(t, "second question", entries[2].Content)
	assert.Equal(t, "second reply", entries[3].Content)

	limited := getKimiSessionHistory(t, &Agent{}, sessionID, 2)
	require.Len(t, limited, 2)
	assert.Equal(t, "second question", limited[0].Content)
	assert.Equal(t, "second reply", limited[1].Content)

	count, summary := parseKimiTranscript(sessionDir)
	assert.Equal(t, 2, count, "Kimi Code 2.0 array content should count user turns")
	assert.Equal(t, "first question", summary)
}

func TestAgentGetSessionHistory_LegacyContextFormat(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	sessionID := "legacy-history"
	sessionDir := filepath.Join(home, ".kimi", "sessions", "project", sessionID)
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	contextJSONL := []byte(
		`{"role":"user","content":"legacy question","timestamp":"2026-09-23T10:00:00Z"}
` +
			`{"role":"tool","content":"ignored"}
` +
			`{"role":"assistant","content":"legacy answer","timestamp":"2026-09-23T10:00:01Z"}
`)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "context.jsonl"), contextJSONL, 0o644))

	entries := getKimiSessionHistory(t, &Agent{}, sessionID, 0)
	require.Len(t, entries, 2)
	assert.Equal(t, "legacy question", entries[0].Content)
	assert.Equal(t, "legacy answer", entries[1].Content)
	assert.Equal(t, time.Date(2026, 9, 23, 10, 0, 1, 0, time.UTC), entries[1].Timestamp)
}

func TestAgentGetSessionHistory_OlderWireMessageFormat(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	sessionID := "older-wire-history"
	sessionDir := filepath.Join(home, ".kimi-code", "sessions", "project", sessionID)
	require.NoError(t, os.MkdirAll(filepath.Join(sessionDir, "agents", "main"), 0o755))
	wire := []byte(
		`{"type":"context.append_message","time":1700000000000,"message":{"role":"user","content":"old question"},"origin":{"kind":"user"}}
` +
			`{"type":"context.append_message","time":1700000001000,"message":{"role":"assistant","content":"old answer"},"origin":{"kind":"assistant"}}
`)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "agents", "main", "wire.jsonl"), wire, 0o644))

	entries := getKimiSessionHistory(t, &Agent{}, sessionID, 0)
	require.Len(t, entries, 2)
	assert.Equal(t, "old question", entries[0].Content)
	assert.Equal(t, "old answer", entries[1].Content)
}

// getKimiSessionHistory deliberately queries the optional capability through
// core.Agent. This lets the regression test run against the pre-fix code and
// fail as an assertion when Kimi does not implement HistoryProvider.
func getKimiSessionHistory(t *testing.T, a *Agent, sessionID string, limit int) []core.HistoryEntry {
	t.Helper()
	var agent core.Agent = a
	provider, ok := agent.(core.HistoryProvider)
	require.True(t, ok, "Kimi agent must implement core.HistoryProvider")
	entries, err := provider.GetSessionHistory(context.Background(), sessionID, limit)
	require.NoError(t, err)
	return entries
}
