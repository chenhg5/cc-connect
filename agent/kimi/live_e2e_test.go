package kimi

// Live end-to-end test against a real Kimi Code CLI binary.
// NOT for CI — run manually with:
//
//	KIMI_LIVE_E2E=1 KIMI_BIN=$HOME/.kimi-code/bin/kimi go test ./agent/kimi/ -run TestLiveE2E -v -timeout 5m
//
// Verifies the full #1561 acceptance chain against the production binary:
// probe -> arg dialect -> meta resume-hint capture -> -r session continuity.

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func liveKimiBin(t *testing.T) string {
	t.Helper()
	if os.Getenv("KIMI_LIVE_E2E") != "1" {
		t.Skip("set KIMI_LIVE_E2E=1 to run the live e2e against a real Kimi Code CLI")
	}
	bin := os.Getenv("KIMI_BIN")
	if bin == "" {
		bin = "kimi"
	}
	return bin
}

func runTurn(t *testing.T, s core.AgentSession, prompt string) string {
	t.Helper()
	require.NoError(t, s.Send(prompt, nil, nil))

	var text strings.Builder
	deadline := time.After(150 * time.Second)
	for {
		select {
		case evt, ok := <-s.Events():
			if !ok {
				return text.String()
			}
			switch evt.Type {
			case core.EventText:
				text.WriteString(evt.Content)
			case core.EventThinking:
				t.Logf("[thinking] %.120s", evt.Content)
			case core.EventToolUse:
				t.Logf("[tool] %s %.80s", evt.ToolName, evt.ToolInput)
			case core.EventError:
				t.Fatalf("turn error: %v", evt.Error)
			case core.EventResult:
				return text.String()
			}
		case <-deadline:
			t.Fatal("turn timed out")
		}
	}
}

func TestLiveE2E_KimiCodeFlavor(t *testing.T) {
	bin := liveKimiBin(t)

	agentInf, err := New(map[string]any{
		"cmd":          bin,
		"work_dir":     t.TempDir(),
		"timeout_mins": 3,
	})
	require.NoError(t, err)
	a := agentInf.(*Agent)

	// 1. Probe: real Kimi Code CLI v0.26 -> modern surface (all false).
	assert.False(t, a.flagSupport.Print, "kimi-code must not probe --print support")
	assert.False(t, a.flagSupport.WorkDir, "kimi-code must not probe --work-dir support")
	assert.False(t, a.flagSupport.Quiet, "kimi-code must not probe --quiet support")
	assert.True(t, a.flagSupport.isModernFlavor())

	ctx := context.Background()
	s, err := a.StartSession(ctx, "")
	require.NoError(t, err)
	defer func() { _ = s.Close() }()

	// 2. Turn 1: establish a memory anchor.
	out1 := runTurn(t, s, "Remember the number 7391. Reply with exactly: remembered")
	t.Logf("turn1 text: %q", out1)
	sid := s.CurrentSessionID()
	require.NotEmpty(t, sid, "meta resume hint must populate the session id after turn 1")
	assert.True(t, strings.HasPrefix(sid, "session_"), "kimi-code session id shape, got %q", sid)

	// 3. Turn 2: resume with -r and recall the anchor.
	out2 := runTurn(t, s, "What is the number I asked you to remember? Reply with just the number.")
	t.Logf("turn2 text: %q", out2)
	assert.Contains(t, out2, "7391", "resumed session must recall the anchor from turn 1")
	assert.Equal(t, sid, s.CurrentSessionID(), "session id must stay stable across resume")
}

func TestLiveE2E_KimiCodeQuietArgs(t *testing.T) {
	bin := liveKimiBin(t)
	agentInf, err := New(map[string]any{"cmd": bin, "work_dir": t.TempDir()})
	require.NoError(t, err)
	a := agentInf.(*Agent)

	ks, err := newKimiSession(context.Background(), a.cmd, nil, "/tmp", "", "quiet", "session_x", nil, 0, a.flagSupport)
	require.NoError(t, err)
	defer func() { _ = ks.Close() }()

	args := ks.buildArgs("hello")
	joined := strings.Join(args, " ")
	for _, banned := range []string{"--print", "--quiet", "--work-dir", "--resume", "--yolo", "--auto"} {
		assert.NotContains(t, joined, banned, "kimi-code rejects %s", banned)
	}
	assert.Contains(t, args, "-r")
	assert.Contains(t, args, "--output-format")
	assert.Contains(t, args, "--prompt")
}
