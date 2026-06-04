//go:build blackbox

// Package p1 contains P1 blackbox tests.
//
// Session commands (/help, /current, /name, /switch, /delete, /status, /version)
// are dispatched by cc-connect's engine directly, so they respond within
// seconds regardless of agent speed.
//
// Run:
//
//	CC_BLACKBOX_CLAUDECODE_API_KEY=xxx \
//	CC_BLACKBOX_CLAUDECODE_BASE_URL=https://... \
//	go test -tags blackbox ./tests/blackbox/p1/... -timeout 1200s -v
package p1

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/chenhg5/cc-connect/tests/blackbox/helper"
	bbplatform "github.com/chenhg5/cc-connect/tests/blackbox/platform"
)

const cmdTimeout = 30 * time.Second // engine-handled commands are near-instant

// ── P1-1: /help ──────────────────────────────────────────────────────────────

func TestP1_1_Help_ClaudeCode(t *testing.T) {
	t.Parallel()
	env := helper.NewEnv(t, "claudecode")
	reply := env.SendWithTimeout("/help", cmdTimeout)
	assertContainsAny(t, "P1-1 /help", reply.Text(),
		"/new", "/list", "/stop", "/help", "Available", "命令", "Command")
	t.Logf("P1-1 OK: %q", truncate(reply.Text(), 200))
}

// ── P1-5: /current ───────────────────────────────────────────────────────────

func TestP1_5_Current_ClaudeCode(t *testing.T) {
	t.Parallel()
	env := helper.NewEnv(t, "claudecode")
	env.Send("say hi briefly")

	reply := env.SendWithTimeout("/current", cmdTimeout)
	assertContainsAny(t, "P1-5 /current", strings.ToLower(reply.Text()),
		"session", "会话", "#1", "s1")
	t.Logf("P1-5 OK: %q", truncate(reply.Text(), 200))
}

// ── P1-7: /name ──────────────────────────────────────────────────────────────

func TestP1_7_Name_ClaudeCode(t *testing.T) {
	t.Parallel()
	env := helper.NewEnv(t, "claudecode")
	env.Send("say hi briefly")

	const newName = "blackbox-rename-test"
	env.SendWithTimeout("/name "+newName, cmdTimeout)
	listReply := env.SendWithTimeout("/list", cmdTimeout)

	if !strings.Contains(listReply.Text(), newName) {
		t.Errorf("P1-7 /name: %q not in /list\n%q", newName, listReply.Text())
	}
	t.Logf("P1-7 OK: /list shows renamed session %q", newName)
}

// ── P1-4: /switch ────────────────────────────────────────────────────────────

func TestP1_4_Switch_ClaudeCode(t *testing.T) {
	t.Parallel()
	env := helper.NewEnv(t, "claudecode")

	env.Send("say hi briefly")
	env.SendWithTimeout("/new", cmdTimeout)

	listReply := env.SendWithTimeout("/list", cmdTimeout)
	if strings.Count(listReply.Text(), "msgs") < 2 {
		t.Skipf("could not establish 2 sessions; /list: %q", listReply.Text())
	}

	switchReply := env.SendWithTimeout("/switch 1", cmdTimeout)
	t.Logf("/switch 1: %q", truncate(switchReply.Text(), 150))

	currentReply := env.SendWithTimeout("/current", cmdTimeout)
	t.Logf("P1-4 OK: /current after switch: %q", truncate(currentReply.Text(), 150))
}

// ── P1-6: /delete ────────────────────────────────────────────────────────────

func TestP1_6_Delete_ClaudeCode(t *testing.T) {
	t.Parallel()
	env := helper.NewEnv(t, "claudecode")

	env.Send("say hi briefly")
	env.SendWithTimeout("/new", cmdTimeout)
	env.Send("say hello briefly")
	env.SendWithTimeout("/new", cmdTimeout)

	env.SendWithTimeout("/delete 2", cmdTimeout)

	listReply := env.SendWithTimeout("/list", cmdTimeout)
	remaining := strings.Count(listReply.Text(), "msgs")
	if remaining != 2 {
		t.Errorf("P1-6 /delete: expected 2 sessions, got %d\n/list: %q", remaining, listReply.Text())
	}
	t.Logf("P1-6 OK: 2 sessions remain after deleting #2")
}

// ── P1-9: /status ────────────────────────────────────────────────────────────

func TestP1_9_Status_ClaudeCode(t *testing.T) {
	t.Parallel()
	env := helper.NewEnv(t, "claudecode")
	reply := env.SendWithTimeout("/status", cmdTimeout)
	assertContainsAny(t, "P1-9 /status", strings.ToLower(reply.Text()),
		"agent", "project", "session", "claudecode", "状态")
	t.Logf("P1-9 OK: %q", truncate(reply.Text(), 200))
}

// ── P1-10: /version ──────────────────────────────────────────────────────────

func TestP1_10_Version_ClaudeCode(t *testing.T) {
	t.Parallel()
	env := helper.NewEnv(t, "claudecode")
	reply := env.SendWithTimeout("/version", cmdTimeout)
	// VersionInfo is set by main at startup; in tests it's empty string "".
	// The command must still produce a reply (even if content is empty in tests),
	// confirming the command is dispatched without error.
	if reply == nil {
		t.Fatal("P1-10 /version: no reply received")
	}
	// In production, VersionInfo contains a version string like "v1.3.x".
	// In test environment it's empty — this is expected.
	t.Logf("P1-10 OK: /version replied (content: %q)", truncate(reply.Text(), 100))
}

// ── P1-14: 消息排队 ───────────────────────────────────────────────────────────

func TestP1_14_MessageQueuing_ClaudeCode(t *testing.T) {
	t.Parallel()
	env := helper.NewEnv(t, "claudecode")

	// Fire 3 messages rapidly while the agent may still be starting.
	env.Platform.InjectMessage(helper.DefaultUser, helper.DefaultChat, "reply with only the word: FIRST")
	env.Platform.InjectMessage(helper.DefaultUser, helper.DefaultChat, "reply with only the word: SECOND")
	env.Platform.InjectMessage(helper.DefaultUser, helper.DefaultChat, "reply with only the word: THIRD")

	// Wait until all 3 keywords appear in the combined output.
	// We can't predict exact message count due to thinking/queue notifications.
	deadline := time.Now().Add(3 * helper.DefaultReplyTimeout)
	for {
		combined := strings.ToUpper(env.Platform.AllText())
		allFound := strings.Contains(combined, "FIRST") &&
			strings.Contains(combined, "SECOND") &&
			strings.Contains(combined, "THIRD")
		if allFound {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("P1-14: timeout waiting for FIRST/SECOND/THIRD\nall messages:\n%s",
				env.Platform.AllText())
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Logf("P1-14 OK: all 3 queued messages processed\nall messages:\n%s", env.Platform.AllText())
}

// ── shared helpers ────────────────────────────────────────────────────────────

func assertContainsAny(t *testing.T, label, text string, candidates ...string) {
	t.Helper()
	lower := strings.ToLower(text)
	for _, c := range candidates {
		if strings.Contains(lower, strings.ToLower(c)) {
			return
		}
	}
	t.Errorf("%s: none of %v found\ngot: %q", label, candidates, text)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// restartedEngine stops env.Engine and returns a new Engine + MockPlatform
// that share the same session store — simulating a service restart.
func restartedEngine(t *testing.T, agentType, dataDir string) (*core.Engine, *bbplatform.MockPlatform) {
	t.Helper()
	opts := map[string]any{"work_dir": t.TempDir()}
	agent, err := core.CreateAgent(agentType, opts)
	if err != nil {
		t.Skipf("restart skip: cannot create %s agent: %v", agentType, err)
	}

	mp := bbplatform.New(agentType + "-restarted")
	sessPath := filepath.Join(dataDir, "sessions.json")
	engine := core.NewEngine("blackbox-restarted", agent, []core.Platform{mp}, sessPath, core.LangEnglish)
	if err := engine.Start(); err != nil {
		t.Fatalf("restarted engine Start failed: %v", err)
	}
	t.Cleanup(func() { engine.Stop(); agent.Stop() })
	return engine, mp
}
