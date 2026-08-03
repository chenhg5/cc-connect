package grok

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseGrokInspectSkillsFiltersAndFirstWins(t *testing.T) {
	commands, err := parseGrokNativeSlashCommands([]byte(`{
  "skills": [
    {"name":"review","description":"first","source":{"type":"bundled","path":"/bundled/review/SKILL.md"},"userInvocable":true},
    {"name":"hidden","description":"hidden","source":{"path":"/user/hidden/SKILL.md"},"userInvocable":false},
    {"name":"disabled","description":"disabled","source":{"path":"/user/disabled/SKILL.md"},"userInvocable":true,"enabled":false},
    {"name":"review","description":"plugin duplicate","source":{"type":"plugin","plugin_name":"tools","path":"/plugins/tools/review/SKILL.md"},"userInvocable":true},
    {"name":"legacy","description":"flat","source":{"type":"project","path":"/repo/.grok/commands/legacy.md"},"userInvocable":true}
  ]
}`))
	require.NoError(t, err)
	require.Len(t, commands, 2)
	assert.Equal(t, core.NativeSlashCommand{Name: "review", Target: "review", Description: "first", IsSkill: true}, commands[0])
	assert.Equal(t, core.NativeSlashCommand{Name: "legacy", Target: "legacy", Description: "flat"}, commands[1])
}

func TestParseGrokInspectRequiresSkillsField(t *testing.T) {
	_, err := parseGrokNativeSlashCommands([]byte(`{}`))
	require.Error(t, err)

	commands, err := parseGrokNativeSlashCommands([]byte(`{"skills":[]}`))
	require.NoError(t, err)
	assert.Empty(t, commands)
}

func TestParseGrokSlashInitRequiresAdvertisedFields(t *testing.T) {
	_, err := parseGrokSlashInit([]byte(`{"type":"system","subtype":"init","session_id":"id"}`))
	require.Error(t, err)

	frame, err := parseGrokSlashInit([]byte(`{"type":"system","subtype":"init","session_id":"id","slash_commands":[],"skills":[]}`))
	require.NoError(t, err)
	assert.Equal(t, "id", frame.SessionID)
}

func TestNativeCommandsFromGrokInitUsesExactQualifiedTargets(t *testing.T) {
	slash := []string{"compact", "skill", "always-approve", "auto", "hooks-list", "feedback", "user:review", "plugin-tools:review", "USER:REVIEW"}
	skills := []string{"skill", "user:review", "plugin-tools:review"}
	frame := grokSlashInit{SlashCommands: &slash, Skills: &skills}
	inspect := []grokInspectSkill{
		{Description: "reidentified skill"},
		{Description: "user review"},
		{Description: "plugin review"},
	}

	commands := nativeCommandsFromGrokInit(frame, inspect)
	require.Len(t, commands, 8) // case-insensitive duplicate is rejected
	assert.Equal(t, "compact", commands[0].Target)
	assert.Equal(t, "compress", commands[0].PolicyCommand)
	assert.Equal(t, "compress", commands[0].ReplacementCommand)
	assert.True(t, commands[1].IsSkill)
	assert.Equal(t, "skills", commands[1].PolicyCommand)
	assert.Equal(t, "skills", commands[1].ReplacementCommand)
	assert.Equal(t, "always-approve", commands[2].Target)
	assert.True(t, commands[2].AdminOnly)
	assert.Equal(t, "mode yolo", commands[2].ReplacementCommand)
	assert.Equal(t, "auto", commands[3].Target)
	assert.True(t, commands[3].AdminOnly)
	assert.Equal(t, "mode auto", commands[3].ReplacementCommand)
	assert.Equal(t, "user:review", commands[6].Target)
	assert.Equal(t, "user review", commands[6].Description)
	assert.Equal(t, "plugin-tools:review", commands[7].Target)
	assert.True(t, commands[7].IsSkill)
}

func TestGrokUsesNativeCompactCommand(t *testing.T) {
	agent := &Agent{}
	assert.Equal(t, "/compact", agent.CompressCommand())
	var _ core.ContextCompressor = agent
}

func TestNativeSlashPolicyAvailableWithoutDiscovery(t *testing.T) {
	agent := &Agent{}
	command, ok := agent.NativeSlashPolicy("always_approve")
	require.True(t, ok)
	assert.Equal(t, "always-approve", command.Target)
	assert.True(t, command.AdminOnly)
	assert.Equal(t, "mode", command.PolicyCommand)
	assert.Equal(t, "mode yolo", command.ReplacementCommand)

	command, ok = agent.NativeSlashPolicy("compact")
	require.True(t, ok)
	assert.False(t, command.AdminOnly)
	assert.Equal(t, "compress", command.PolicyCommand)
	assert.Equal(t, "compress", command.ReplacementCommand)

	_, ok = agent.NativeSlashPolicy("project:review")
	assert.False(t, ok)
}

func TestNativeSlashCommandsRunsExactProbeAndCleansKnownSession(t *testing.T) {
	root := t.TempDir()
	realWorkDir := filepath.Join(root, "real")
	require.NoError(t, os.MkdirAll(realWorkDir, 0o755))
	linkedWorkDir := filepath.Join(root, "linked")
	require.NoError(t, os.Symlink(realWorkDir, linkedWorkDir))
	grokHome := filepath.Join(root, "grok-home")
	require.NoError(t, os.MkdirAll(grokHome, 0o755))

	type invocation struct {
		command string
		args    []string
		cwd     string
		env     []string
	}
	var mu sync.Mutex
	var calls []invocation
	runner := func(_ context.Context, command string, args []string, cwd string, env []string) ([]byte, error) {
		mu.Lock()
		calls = append(calls, invocation{command: command, args: append([]string(nil), args...), cwd: cwd, env: append([]string(nil), env...)})
		mu.Unlock()
		switch {
		case sliceContains(args, "inspect"):
			return []byte(`{"skills":[{"name":"review","description":"Review exactly","source":{"path":"/x/SKILL.md"},"userInvocable":true}]}`), nil
		case sliceContains(args, "delete"):
			return nil, nil
		default:
			sessionID := valueAfter(args, "--session-id")
			return []byte(`{"type":"system","subtype":"init","session_id":"` + sessionID + `","slash_commands":["review"],"skills":["review"]}` + "\n"), nil
		}
	}

	agent := &Agent{
		workDir:       linkedWorkDir,
		cmd:           "grok-wrapper",
		cliExtraArgs:  []string{"--wrapper-flag"},
		configEnv:     []string{"GROK_HOME=" + grokHome, "CC_NATIVE_TEST=value"},
		activeIdx:     -1,
		inspectRunner: runner,
	}
	commands, err := agent.NativeSlashCommands(context.Background())
	require.NoError(t, err)
	require.Equal(t, []core.NativeSlashCommand{{Name: "review", Target: "review", Description: "Review exactly", IsSkill: true}}, commands)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, calls, 3)
	canonical, err := filepath.EvalSymlinks(realWorkDir)
	require.NoError(t, err)
	for _, call := range calls {
		assert.Equal(t, "grok-wrapper", call.command)
		assert.Equal(t, canonical, call.cwd)
		assert.Contains(t, call.env, "CC_NATIVE_TEST=value")
	}
	assert.Equal(t, []string{"--wrapper-flag", "--no-auto-update", "inspect", "--json"}, calls[0].args)
	probeID := valueAfter(calls[1].args, "--session-id")
	require.NotEmpty(t, probeID)
	assert.Equal(t, canonical, valueAfter(calls[1].args, "--cwd"))
	assert.Equal(t, "plan", valueAfter(calls[1].args, "--permission-mode"))
	assert.Equal(t, "streaming-messages-json", valueAfter(calls[1].args, "--output-format"))
	assert.Equal(t, "/session-info", valueAfter(calls[1].args, "-p"))
	assert.Equal(t, []string{"--wrapper-flag", "--no-auto-update", "sessions", "delete", probeID}, calls[2].args)
}

func TestNativeSlashCommandsFallsBackToInspectAndRedactsErrors(t *testing.T) {
	workDir := t.TempDir()
	grokHome := filepath.Join(t.TempDir(), "grok-home")
	require.NoError(t, os.MkdirAll(grokHome, 0o755))
	call := 0
	agent := &Agent{
		workDir:   workDir,
		cmd:       "grok",
		configEnv: []string{"GROK_HOME=" + grokHome},
		activeIdx: -1,
	}
	agent.inspectRunner = func(_ context.Context, _ string, args []string, _ string, _ []string) ([]byte, error) {
		call++
		if sliceContains(args, "inspect") {
			return []byte(`{"skills":[{"name":"fallback","description":"fallback","source":{"path":"/x/SKILL.md"},"userInvocable":true}]}`), nil
		}
		return nil, errors.New("SECRET_TOKEN=/private/path")
	}
	commands, err := agent.NativeSlashCommands(context.Background())
	require.NoError(t, err)
	require.Len(t, commands, 1)
	assert.Equal(t, "fallback", commands[0].Target)
	assert.GreaterOrEqual(t, call, 3) // inspect, probe, unconditional cleanup

	agent.inspectRunner = func(context.Context, string, []string, string, []string) ([]byte, error) {
		return nil, errors.New("SECRET_TOKEN=/private/path")
	}
	_, err = agent.NativeSlashCommands(context.Background())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "SECRET_TOKEN")
	assert.NotContains(t, err.Error(), "/private/path")
}

func sliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func valueAfter(values []string, target string) string {
	for index := 0; index+1 < len(values); index++ {
		if values[index] == target {
			return values[index+1]
		}
	}
	return ""
}
