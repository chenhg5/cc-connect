//go:build blackbox

package p0

import (
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/tests/blackbox/helper"
)

// ── P0-11: 上下文保持（最核心场景）──────────────────────────────────────────
//
// This is arguably the most important test in the suite. It validates that:
//   1. cc-connect maintains a persistent session across multiple messages
//   2. the real agent (Claude Code) retains conversation context
//   3. the session key routing is correct end-to-end
//
// If context retention breaks, users cannot have multi-turn conversations —
// the entire product stops working for its core use case.

// TestP0_11_ContextRetention_ClaudeCode sends a name in turn 1 and verifies
// the agent recalls it in turn 2. This is only possible with a real persistent
// session — a fake agent would need the name hardcoded.
func TestP0_11_ContextRetention_ClaudeCode(t *testing.T) {
	t.Parallel()
	testContextRetention(t, "claudecode")
}

func TestP0_11_ContextRetention_Codex(t *testing.T) {
	t.Parallel()
	testContextRetention(t, "codex")
}

func TestP0_11_ContextRetention_Cursor(t *testing.T) {
	t.Parallel()
	testContextRetention(t, "cursor")
}

func TestP0_11_ContextRetention_OpenCode(t *testing.T) {
	t.Parallel()
	testContextRetention(t, "opencode")
}

func testContextRetention(t *testing.T, agentType string) {
	t.Helper()
	env := helper.NewEnv(t, agentType)

	// Turn 1: plant a unique fact. Use SendComplete so we wait for the full
	// agent turn before sending turn 2 — otherwise the second message gets
	// queued while the agent is still processing and loses context.
	const uniqueName = "ContextTestMarker42"
	turn1 := env.SendComplete("My name is " + uniqueName + ". Just acknowledge it briefly.")

	turn1Text := helper.AnyText(turn1)
	if strings.TrimSpace(turn1Text) == "" {
		t.Fatalf("turn 1: empty reply; all messages:\n%s", env.Platform.AllText())
	}
	t.Logf("turn 1 (%d msgs): %q", len(turn1), truncate(turn1Text, 200))

	// Turn 2: ask the agent to recall the fact.
	turn2 := env.SendComplete("What is my name? Reply with just the name.")

	turn2Text := helper.AnyText(turn2)
	if strings.TrimSpace(turn2Text) == "" {
		t.Fatalf("turn 2: empty reply; all messages:\n%s", env.Platform.AllText())
	}
	t.Logf("turn 2 (%d msgs): %q", len(turn2), truncate(turn2Text, 200))

	// The agent must recall the name planted in turn 1.
	if !strings.Contains(strings.ToLower(turn2Text), strings.ToLower(uniqueName)) {
		t.Errorf(
			"context retention FAILED: turn 2 does not contain %q\n"+
				"turn 1: %q\n"+
				"turn 2: %q\n"+
				"all messages:\n%s",
			uniqueName, turn1Text, turn2Text, env.Platform.AllText(),
		)
	} else {
		t.Logf("P0-11 OK: agent recalled %q in turn 2", uniqueName)
	}
}

// TestP0_11_ContextIsolation_ClaudeCode verifies that two different users
// (different session keys) have completely independent sessions.
// User A's context must NOT appear in User B's session.
func TestP0_11_ContextIsolation_ClaudeCode(t *testing.T) {
	t.Parallel()
	testContextIsolation(t, "claudecode")
}

func testContextIsolation(t *testing.T, agentType string) {
	t.Helper()
	env := helper.NewEnv(t, agentType)

	const nameA = "AliceIsolationTest"
	const nameB = "BobIsolationTest"

	// User A plants their name (wait for full turn).
	msgsA1 := env.SendCompleteAs("userA", "chat1", "My name is "+nameA+". Just say ok.", 5*time.Second, helper.DefaultReplyTimeout)
	t.Logf("userA turn 1: %q", truncate(helper.AnyText(msgsA1), 150))

	// User B (different session) plants their name (wait for full turn).
	msgsB1 := env.SendCompleteAs("userB", "chat1", "My name is "+nameB+". Just say ok.", 5*time.Second, helper.DefaultReplyTimeout)
	t.Logf("userB turn 1: %q", truncate(helper.AnyText(msgsB1), 150))

	// User A asks — should see only their own name.
	msgsA2 := env.SendCompleteAs("userA", "chat1", "What is my name?", 5*time.Second, helper.DefaultReplyTimeout)
	textA2 := helper.AnyText(msgsA2)
	t.Logf("userA turn 2: %q", truncate(textA2, 150))

	if !strings.Contains(strings.ToLower(textA2), strings.ToLower(nameA)) {
		t.Errorf("userA context lost: expected %q in reply, got: %q", nameA, textA2)
	}
	if strings.Contains(strings.ToLower(textA2), strings.ToLower(nameB)) {
		t.Errorf("session leak: userA reply contains userB's name %q: %q", nameB, textA2)
	}
	t.Logf("P0-11 isolation OK")
}
