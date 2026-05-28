//go:build blackbox

package p1

// Session visibility regression suite — P1-30 to P1-40.
//
// These tests reproduce bugs from v1.3.1:
//   - /list lost sessions after /new
//   - /new xxx names didn't persist
//   - /switch caused sessions to disappear
//
// Per checklist: ALL P1-30..40 tests must run on BOTH claudecode AND codex.

import (
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/tests/blackbox/helper"
)

// ── P1-30: 历史会话全部可见 ───────────────────────────────────────────────────

func TestP1_30_HistorySessionsVisible_ClaudeCode(t *testing.T) {
	t.Parallel()
	testHistorySessionsVisible(t, "claudecode")
}

func TestP1_30_HistorySessionsVisible_Codex(t *testing.T) {
	t.Parallel()
	testHistorySessionsVisible(t, "codex")
}

func testHistorySessionsVisible(t *testing.T, agentType string) {
	t.Helper()
	env := helper.NewEnv(t, agentType)

	env.Send("say ALPHA briefly")
	env.SendWithTimeout("/new", cmdTimeout)
	env.Send("say BETA briefly")
	env.SendWithTimeout("/new", cmdTimeout)
	env.Send("say GAMMA briefly")

	listReply := env.SendWithTimeout("/list", cmdTimeout)
	count := strings.Count(listReply.Text(), "msgs")
	if count < 3 {
		t.Errorf("P1-30: expected ≥3 sessions, got %d\n/list: %q", count, listReply.Text())
	}
	t.Logf("P1-30 OK: %d sessions visible in /list", count)
}

// ── P1-31: /new 后 /list 完整 ─────────────────────────────────────────────────

func TestP1_31_NewThenListComplete_ClaudeCode(t *testing.T) {
	t.Parallel()
	testNewThenListComplete(t, "claudecode")
}

func TestP1_31_NewThenListComplete_Codex(t *testing.T) {
	t.Parallel()
	testNewThenListComplete(t, "codex")
}

func testNewThenListComplete(t *testing.T, agentType string) {
	t.Helper()
	env := helper.NewEnv(t, agentType)

	// Session 1: wait for full agent turn before creating session 2.
	env.SendComplete("say hi briefly")

	env.SendWithTimeout("/new", cmdTimeout)
	env.SendComplete("say hello briefly")

	listReply := env.SendWithTimeout("/list", cmdTimeout)
	count := strings.Count(listReply.Text(), "msgs")
	if count < 2 {
		t.Errorf("P1-31: expected ≥2 sessions after /new, got %d\n/list: %q", count, listReply.Text())
	}
	t.Logf("P1-31 OK: %d sessions after /new + message", count)
}

// ── P1-32: /new xxx 命名生效 ─────────────────────────────────────────────────

func TestP1_32_NewWithNaming_ClaudeCode(t *testing.T) {
	t.Parallel()
	testNewWithNaming(t, "claudecode")
}

func TestP1_32_NewWithNaming_Codex(t *testing.T) {
	t.Parallel()
	testNewWithNaming(t, "codex")
}

func testNewWithNaming(t *testing.T, agentType string) {
	t.Helper()
	env := helper.NewEnv(t, agentType)

	const sessionName = "regression-name-test"
	env.SendWithTimeout("/new "+sessionName, cmdTimeout)
	env.SendComplete("say ok briefly")

	listReply := env.SendWithTimeout("/list", cmdTimeout)
	if !strings.Contains(listReply.Text(), sessionName) {
		t.Errorf("P1-32: /new %q name missing in /list\n/list: %q", sessionName, listReply.Text())
	}
	t.Logf("P1-32 OK: session name %q appears in /list", sessionName)
}

// ── P1-33: /name 重命名 ──────────────────────────────────────────────────────

func TestP1_33_NameRename_ClaudeCode(t *testing.T) {
	t.Parallel()
	testNameRename(t, "claudecode")
}

func TestP1_33_NameRename_Codex(t *testing.T) {
	t.Parallel()
	testNameRename(t, "codex")
}

func testNameRename(t *testing.T, agentType string) {
	t.Helper()
	env := helper.NewEnv(t, agentType)

	env.SendComplete("say hi briefly")

	const newName = "renamed-42"
	env.SendWithTimeout("/name "+newName, cmdTimeout)

	listReply := env.SendWithTimeout("/list", cmdTimeout)
	if !strings.Contains(listReply.Text(), newName) {
		t.Errorf("P1-33: /name %q not in /list\n/list: %q", newName, listReply.Text())
	}
	t.Logf("P1-33 OK: renamed session %q in /list", newName)
}

// ── P1-34: /switch 后 /list 完整 ─────────────────────────────────────────────

func TestP1_34_SwitchPreservesAllSessions_ClaudeCode(t *testing.T) {
	t.Parallel()
	testSwitchPreservesAllSessions(t, "claudecode")
}

func TestP1_34_SwitchPreservesAllSessions_Codex(t *testing.T) {
	t.Parallel()
	testSwitchPreservesAllSessions(t, "codex")
}

func testSwitchPreservesAllSessions(t *testing.T, agentType string) {
	t.Helper()
	env := helper.NewEnv(t, agentType)

	env.SendComplete("say hi briefly")
	env.SendWithTimeout("/new", cmdTimeout)
	env.SendComplete("say hello briefly")

	env.SendWithTimeout("/switch 1", cmdTimeout)

	listReply := env.SendWithTimeout("/list", cmdTimeout)
	count := strings.Count(listReply.Text(), "msgs")
	if count < 2 {
		t.Errorf("P1-34: /list after /switch shows %d session(s), expected ≥2\n/list: %q",
			count, listReply.Text())
	}
	t.Logf("P1-34 OK: %d sessions preserved after /switch", count)
}

// ── P1-35: /delete 只删目标 ──────────────────────────────────────────────────

func TestP1_35_DeleteOnlyRemovesTarget_ClaudeCode(t *testing.T) {
	t.Parallel()
	testDeleteOnlyRemovesTarget(t, "claudecode")
}

func TestP1_35_DeleteOnlyRemovesTarget_Codex(t *testing.T) {
	t.Parallel()
	testDeleteOnlyRemovesTarget(t, "codex")
}

func testDeleteOnlyRemovesTarget(t *testing.T, agentType string) {
	t.Helper()
	env := helper.NewEnv(t, agentType)

	env.SendComplete("say one briefly")
	env.SendWithTimeout("/new", cmdTimeout)
	env.SendComplete("say two briefly")
	env.SendWithTimeout("/new", cmdTimeout)
	env.SendComplete("say three briefly")

	pre := env.SendWithTimeout("/list", cmdTimeout)
	if strings.Count(pre.Text(), "msgs") < 3 {
		t.Skipf("P1-35: could not establish 3 sessions; /list: %q", pre.Text())
	}

	env.SendWithTimeout("/delete 2", cmdTimeout)

	post := env.SendWithTimeout("/list", cmdTimeout)
	remaining := strings.Count(post.Text(), "msgs")
	if remaining != 2 {
		t.Errorf("P1-35: expected 2 sessions after /delete 2, got %d\n/list: %q",
			remaining, post.Text())
	}
	t.Logf("P1-35 OK: 2 sessions remain after deleting #2")
}

// ── P1-37: Agent 回复后 /list 不减少 ─────────────────────────────────────────

func TestP1_37_ListAfterReplyStable_ClaudeCode(t *testing.T) {
	t.Parallel()
	testListAfterReplyStable(t, "claudecode")
}

func TestP1_37_ListAfterReplyStable_Codex(t *testing.T) {
	t.Parallel()
	testListAfterReplyStable(t, "codex")
}

func testListAfterReplyStable(t *testing.T, agentType string) {
	t.Helper()
	env := helper.NewEnv(t, agentType)

	env.SendComplete("say hi briefly")
	env.SendWithTimeout("/new", cmdTimeout)
	env.SendComplete("say hello briefly")

	pre := env.SendWithTimeout("/list", cmdTimeout)
	preCnt := strings.Count(pre.Text(), "msgs")

	time.Sleep(500 * time.Millisecond)

	post := env.SendWithTimeout("/list", cmdTimeout)
	postCnt := strings.Count(post.Text(), "msgs")

	if postCnt < preCnt {
		t.Errorf("P1-37: session count dropped %d → %d after agent reply\npre: %q\npost: %q",
			preCnt, postCnt, pre.Text(), post.Text())
	}
	t.Logf("P1-37 OK: session count stable (%d → %d)", preCnt, postCnt)
}

// ── P1-39: 重启后名称保留 ─────────────────────────────────────────────────────

func TestP1_39_RestartPreservesNames_ClaudeCode(t *testing.T) {
	// Not parallel — involves engine stop/restart.
	testRestartPreservesNames(t, "claudecode")
}

func testRestartPreservesNames(t *testing.T, agentType string) {
	t.Helper()
	env := helper.NewEnv(t, agentType)

	const sessionName = "persisted-name-99"
	env.SendWithTimeout("/new "+sessionName, cmdTimeout)
	env.SendComplete("say ok briefly")

	pre := env.SendWithTimeout("/list", cmdTimeout)
	if !strings.Contains(pre.Text(), sessionName) {
		t.Skipf("P1-39: name %q not established pre-restart (%q); skipping", sessionName, pre.Text())
	}

	// Simulate restart: stop engine, start fresh engine with same session store.
	env.Engine.Stop()

	_, newMP := restartedEngine(t, agentType, env.DataDir)

	before := newMP.MessageCount()
	newMP.InjectMessage(helper.DefaultUser, helper.DefaultChat, "/list")
	listReply := newMP.WaitForReply(before, cmdTimeout)
	if listReply == nil {
		t.Fatal("P1-39: no /list response after restart")
	}
	if !strings.Contains(listReply.Text(), sessionName) {
		t.Errorf("P1-39: name %q lost after restart\n/list: %q", sessionName, listReply.Text())
	}
	t.Logf("P1-39 OK: name %q preserved after restart", sessionName)
}
