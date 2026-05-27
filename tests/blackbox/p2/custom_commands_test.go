//go:build blackbox

package p2

// Custom commands and aliases — P2-63 to P2-70.
//
// These tests exercise the /commands and /alias engine features:
//   P2-63: /commands add <name> <prompt>
//   P2-64: invoke the custom command
//   P2-65: /commands list
//   P2-66: /commands del <name>
//   P2-67: /commands addexec <name> <shell-cmd>
//   P2-68: invoke the exec command
//   P2-69: /alias add <alias> <target>
//   P2-70: /alias del <alias>

import (
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/tests/blackbox/helper"
)

// TestP2_63_65_66_CustomCommandLifecycle tests the full custom prompt command
// lifecycle: add → list → invoke → delete → verify deleted.
func TestP2_63_65_66_CustomCommandLifecycle_ClaudeCode(t *testing.T) {
	t.Parallel()
	env := helper.NewEnv(t, "claudecode")

	const cmdName = "bb-test-cmd"
	const cmdPrompt = "Say the word BLACKBOX_MARKER"

	// P2-63: add.
	addReply := env.SendWithTimeout("/commands add "+cmdName+" "+cmdPrompt, p2CmdTimeout)
	t.Logf("P2-63 /commands add: %q", truncate(addReply.Text(), 150))
	assertContainsAny(t, "P2-63 /commands add", strings.ToLower(addReply.Text()),
		"created", "added", "success", "成功", cmdName)

	// P2-65: list — new command should appear.
	listReply := env.SendWithTimeout("/commands", p2CmdTimeout)
	t.Logf("P2-65 /commands: %q", truncate(listReply.Text(), 200))
	if !strings.Contains(listReply.Text(), cmdName) {
		t.Errorf("P2-65: %q not in /commands list\n%q", cmdName, listReply.Text())
	}

	// P2-64: invoke the custom command.
	invokeReply := env.Send("/" + cmdName)
	t.Logf("P2-64 /%s: %q", cmdName, truncate(invokeReply.Text(), 200))
	// The agent should say something in response to the prompt.
	if strings.TrimSpace(invokeReply.Text()) == "" {
		t.Errorf("P2-64: /%s produced empty reply", cmdName)
	}

	// P2-66: delete.
	delReply := env.SendWithTimeout("/commands del "+cmdName, p2CmdTimeout)
	t.Logf("P2-66 /commands del: %q", truncate(delReply.Text(), 150))

	// Verify it's gone from the list.
	listAfter := env.SendWithTimeout("/commands", p2CmdTimeout)
	if strings.Contains(listAfter.Text(), cmdName) {
		t.Errorf("P2-66: %q still in /commands list after del\n%q", cmdName, listAfter.Text())
	}
	t.Logf("P2-63..66 OK: custom command lifecycle complete")
}

// TestP2_67_68_ExecCommandLifecycle tests adding a shell exec command and
// verifying its output arrives correctly.
func TestP2_67_68_ExecCommandLifecycle_ClaudeCode(t *testing.T) {
	t.Parallel()
	env := helper.NewEnv(t, "claudecode")

	const execName = "bb-exec-test"

	// P2-67: add exec command (echo a unique marker).
	addReply := env.SendWithTimeout(
		"/commands addexec "+execName+" echo EXEC_MARKER_77",
		p2CmdTimeout,
	)
	t.Logf("P2-67 /commands addexec: %q", truncate(addReply.Text(), 150))
	assertContainsAny(t, "P2-67 addexec", strings.ToLower(addReply.Text()),
		"created", "added", "success", "成功", execName)

	// P2-68: invoke exec command.
	execReply := env.SendWithTimeout("/"+execName, p2CmdTimeout)
	t.Logf("P2-68 /%s: %q", execName, truncate(execReply.Text(), 200))

	if !strings.Contains(execReply.Text(), "EXEC_MARKER_77") {
		t.Errorf("P2-68: /%s output missing EXEC_MARKER_77\ngot: %q", execName, execReply.Text())
	}

	// Cleanup.
	env.SendWithTimeout("/commands del "+execName, p2CmdTimeout)
	t.Logf("P2-67..68 OK: exec command invoked, output correct")
}

// TestP2_69_70_AliasLifecycle tests adding and removing an alias.
func TestP2_69_70_AliasLifecycle_ClaudeCode(t *testing.T) {
	t.Parallel()
	env := helper.NewEnv(t, "claudecode")

	// P2-69: add alias "bbhelp" → "/help".
	addReply := env.SendWithTimeout("/alias add bbhelp /help", p2CmdTimeout)
	t.Logf("P2-69 /alias add: %q", truncate(addReply.Text(), 150))
	assertContainsAny(t, "P2-69 /alias add", strings.ToLower(addReply.Text()),
		"added", "created", "success", "成功", "alias", "bbhelp")

	// Trigger the alias — should behave like /help.
	aliasReply := env.SendWithTimeout("bbhelp", p2CmdTimeout)
	t.Logf("alias bbhelp reply: %q", truncate(aliasReply.Text(), 200))
	assertContainsAny(t, "P2-69 alias invocation", aliasReply.Text(),
		"/new", "/list", "/stop", "/help", "Command", "命令", "Available")

	// P2-70: delete alias.
	delReply := env.SendWithTimeout("/alias del bbhelp", p2CmdTimeout)
	t.Logf("P2-70 /alias del: %q", truncate(delReply.Text(), 150))

	// Verify alias is gone — sending "bbhelp" should NOT trigger /help.
	afterReply := env.Send("bbhelp")
	afterText := strings.ToLower(afterReply.Text())
	// After deletion, "bbhelp" is just a plain message passed to the agent.
	// It should NOT produce a command list (which /help would).
	triggeredHelp := strings.Contains(afterText, "/new") && strings.Contains(afterText, "/list") &&
		strings.Contains(afterText, "/stop")
	if triggeredHelp {
		t.Errorf("P2-70: alias bbhelp still active after /alias del\ngot: %q", afterReply.Text())
	}
	t.Logf("P2-69..70 OK: alias lifecycle complete")
}
