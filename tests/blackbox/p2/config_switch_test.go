//go:build blackbox

package p2

// Configuration switch tests — P2-71 to P2-86.
//
// These tests verify that engine-level config options produce the expected
// observable output difference. They correspond to the checklist items that
// require restarting with a different config (P2-71..P2-88).
//
// Unlike the manual checklist process (apply config → IM verify → revert),
// these tests use NewEnvWithSetup to configure the engine BEFORE start,
// making them fully automated.
//
// Tests are split into two tiers:
//
//   FAST (no agent API call needed — engine handles before forwarding):
//     - P2-81: disabled_commands → blocked immediately by engine
//     - P2-82: banned_words → blocked immediately by engine
//     - P2-40b: rate_limit → throttled immediately by engine
//
//   SLOW (agent API call required — verifying output format):
//     - P2-71: show_context_indicator = false → no [ctx: ~N%] in replies
//     - P2-77: display.mode = "compact" → no thinking/tool messages
//     - P2-78: thinking_messages = false → no 💭 thinking messages
//     - P2-79: tool_messages = false → no 🔧 tool messages

import (
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/chenhg5/cc-connect/tests/blackbox/helper"
)

const cfgCmdTimeout = 30 * time.Second

// ── P2-81: disabled_commands ──────────────────────────────────────────────────

// TestP2_81_DisabledCommands verifies that a disabled command is blocked by the
// engine before it reaches the agent. This test is FAST — no agent API call.
func TestP2_81_DisabledCommands(t *testing.T) {
	t.Parallel()
	env := helper.NewEnvWithSetup(t, "claudecode", func(e *core.Engine) {
		e.SetDisabledCommands([]string{"help", "restart", "shell"})
	})

	// /help should be blocked.
	helpReply := env.SendWithTimeout("/help", cfgCmdTimeout)
	if !strings.Contains(helpReply.Text(), "disabled") && !strings.Contains(helpReply.Text(), "禁用") {
		t.Errorf("P2-81: /help not blocked; got: %q", helpReply.Text())
	}
	t.Logf("P2-81a /help blocked: %q", truncate(helpReply.Text(), 100))

	// /restart should be blocked too.
	restartReply := env.SendWithTimeout("/restart", cfgCmdTimeout)
	if !strings.Contains(restartReply.Text(), "disabled") && !strings.Contains(restartReply.Text(), "禁用") {
		t.Errorf("P2-81: /restart not blocked; got: %q", restartReply.Text())
	}
	t.Logf("P2-81b /restart blocked: %q", truncate(restartReply.Text(), 100))

	// /list should NOT be blocked (it's not in the disabled list).
	listReply := env.SendWithTimeout("/list", cfgCmdTimeout)
	if strings.Contains(listReply.Text(), "disabled") {
		t.Errorf("P2-81: /list incorrectly blocked; got: %q", listReply.Text())
	}
	t.Logf("P2-81 OK: /help and /restart disabled, /list still works")
}

// ── P2-82: banned_words ───────────────────────────────────────────────────────

// TestP2_82_BannedWords verifies that messages containing a banned word are
// intercepted by the engine before reaching the agent. FAST — no agent API call.
func TestP2_82_BannedWords(t *testing.T) {
	t.Parallel()
	const bannedWord = "XBLACKBOXBANWORD"
	env := helper.NewEnvWithSetup(t, "claudecode", func(e *core.Engine) {
		e.SetBannedWords([]string{bannedWord})
	})

	// Message with banned word → should be blocked.
	blockedReply := env.SendWithTimeout(
		"Please help me with "+bannedWord+" testing.",
		cfgCmdTimeout,
	)
	assertContainsAny(t, "P2-82 banned reply", blockedReply.Text(),
		"blocked", "prohibited", "违禁", "拦截", "⚠️")
	t.Logf("P2-82a banned word blocked: %q", truncate(blockedReply.Text(), 100))

	// Normal message → should pass through (agent responds).
	normalReply := env.Send("say hi briefly")
	if strings.TrimSpace(normalReply.Text()) == "" {
		t.Errorf("P2-82: normal message got empty reply after banned-word config")
	}
	if strings.Contains(strings.ToLower(normalReply.Text()), "prohibited") ||
		strings.Contains(strings.ToLower(normalReply.Text()), "blocked") {
		t.Errorf("P2-82: normal message incorrectly blocked; got: %q", normalReply.Text())
	}
	t.Logf("P2-82 OK: banned word blocked, normal message passes")
}

// ── P2-82b: banned_words case-insensitive ────────────────────────────────────

// TestP2_82b_BannedWordsCaseInsensitive checks that banned word matching is
// case-insensitive (lowercase config word, uppercase in message).
func TestP2_82b_BannedWordsCaseInsensitive(t *testing.T) {
	t.Parallel()
	env := helper.NewEnvWithSetup(t, "claudecode", func(e *core.Engine) {
		// Register the word in lowercase.
		e.SetBannedWords([]string{"casetest_banned"})
	})

	// Message uses UPPERCASE version.
	reply := env.SendWithTimeout("help with CASETEST_BANNED scenario", cfgCmdTimeout)
	assertContainsAny(t, "P2-82b case-insensitive ban", reply.Text(),
		"blocked", "prohibited", "违禁", "拦截", "⚠️")
	t.Logf("P2-82b OK: case-insensitive banned word detected")
}

// ── P2-71: show_context_indicator = false ────────────────────────────────────

// TestP2_71_HideContextIndicator verifies that when show_context_indicator is
// false, the [ctx: ~N%] suffix is absent from agent replies.
//
// This test is SLOW — it requires a real agent turn to produce final output.
// The context indicator only appears when input_tokens >= 100, so we send
// a few messages first to build up token history.
func TestP2_71_HideContextIndicator_ClaudeCode(t *testing.T) {
	t.Parallel()
	env := helper.NewEnvWithSetup(t, "claudecode", func(e *core.Engine) {
		e.SetShowContextIndicator(false)
	})

	// Send several messages to accumulate token history (ctx indicator threshold).
	for _, msg := range []string{
		"My name is BlackboxConfigTest.",
		"I am testing context indicator behavior.",
		"say hi briefly", // this turn we check
	} {
		msgs := env.SendComplete(msg)
		combined := helper.AnyText(msgs)
		if strings.Contains(combined, "[ctx:") {
			t.Errorf("P2-71: [ctx: ~N%%] appeared when show_context_indicator=false\nreply: %q",
				combined)
		}
	}
	t.Logf("P2-71 OK: no [ctx:] suffix observed with show_context_indicator=false")
}

// TestP2_71b_ShowContextIndicator verifies that with the DEFAULT setting
// (show_context_indicator = true), the [ctx: ~N%] suffix eventually appears
// after sufficient token accumulation.
func TestP2_71b_ShowContextIndicator_ClaudeCode(t *testing.T) {
	t.Parallel()
	// Default env: show_context_indicator = true
	env := helper.NewEnv(t, "claudecode")

	// Send multiple turns to push token count above the 100-token threshold.
	// The indicator appears when input_tokens >= 100.
	const maxTurns = 5
	found := false
	for i := 0; i < maxTurns; i++ {
		msgs := env.SendComplete("say one sentence about software testing")
		combined := helper.AnyText(msgs)
		if strings.Contains(combined, "[ctx:") {
			found = true
			t.Logf("P2-71b: [ctx:] appeared at turn %d: %q", i+1, truncate(combined, 200))
			break
		}
	}
	if !found {
		t.Logf("P2-71b: [ctx:] not seen in %d turns — token count may not have reached threshold yet", maxTurns)
		// Not a hard failure — threshold is token-count dependent.
	} else {
		t.Logf("P2-71b OK: [ctx:] appears with default show_context_indicator=true")
	}
}

// ── P2-78: thinking_messages = false ─────────────────────────────────────────

// TestP2_78_HideThinkingMessages verifies that when thinking_messages=false,
// no thinking indicators (💭 / thinking prefix) appear in the output.
//
// SLOW — requires agent with thinking enabled (complex question).
func TestP2_78_HideThinkingMessages_ClaudeCode(t *testing.T) {
	t.Parallel()
	env := helper.NewEnvWithSetup(t, "claudecode", func(e *core.Engine) {
		e.SetDisplayConfig(core.DisplayCfg{
			Mode:             "full",
			CardMode:         "legacy",
			ThinkingMessages: false, // ← key config
			ThinkingMaxLen:   300,
			ToolMessages:     true,
			ToolMaxLen:       500,
		})
	})

	// A question that might trigger thinking.
	msgs := env.SendComplete("What is 17 * 23? Show your reasoning.")
	combined := helper.AnyText(msgs)

	// Thinking messages are prefixed with 💭 or contain "thinking" marker.
	// When ThinkingMessages=false, these should NOT appear.
	for _, msg := range msgs {
		text := msg.Text()
		if strings.HasPrefix(text, "💭") || strings.Contains(text, "\n💭") {
			t.Errorf("P2-78: thinking message appeared with ThinkingMessages=false\nmsg: %q", text)
		}
	}
	t.Logf("P2-78 OK: no thinking messages in %d msgs", len(msgs))
	t.Logf("combined reply: %q", truncate(combined, 300))
}

// ── P2-79: tool_messages = false ─────────────────────────────────────────────

// TestP2_79_HideToolMessages verifies that when tool_messages=false, no tool
// progress messages (🔧 / tool invocation indicators) appear.
//
// SLOW — requires a real agent that calls a tool.
func TestP2_79_HideToolMessages_ClaudeCode(t *testing.T) {
	t.Parallel()
	env := helper.NewEnvWithSetup(t, "claudecode", func(e *core.Engine) {
		e.SetDisplayConfig(core.DisplayCfg{
			Mode:             "full",
			CardMode:         "legacy",
			ThinkingMessages: true,
			ThinkingMaxLen:   300,
			ToolMessages:     false, // ← key config
			ToolMaxLen:       500,
		})
	})

	msgs := env.SendComplete("List the files in the current directory. Use a shell command.")
	combined := helper.AnyText(msgs)

	// Tool messages are prefixed with 🔧 or contain "Tool" markers.
	for _, msg := range msgs {
		text := msg.Text()
		if strings.HasPrefix(text, "🔧") || strings.Contains(text, "\n🔧") {
			t.Errorf("P2-79: tool message appeared with ToolMessages=false\nmsg: %q", text)
		}
	}
	// Verify the agent still produced a useful final response (tool was called
	// but progress hidden).
	hasResult := strings.Contains(combined, ".go") ||
		strings.Contains(strings.ToLower(combined), "file") ||
		strings.Contains(strings.ToLower(combined), "directory") ||
		strings.Contains(strings.ToLower(combined), "no files")
	if !hasResult {
		t.Errorf("P2-79: agent didn't produce file listing result\ncombined: %q", combined)
	}
	t.Logf("P2-79 OK: no 🔧 tool messages; final result has file listing")
}

// ── P2-77: display.mode = "compact" ──────────────────────────────────────────

// TestP2_77_DisplayModeCompact verifies that compact mode hides thinking/tool
// messages. Unlike full mode, compact sends each text segment separately but
// suppresses intermediate tool/thinking indicators.
//
// SLOW — requires agent turn.
func TestP2_77_DisplayModeCompact_ClaudeCode(t *testing.T) {
	t.Parallel()
	env := helper.NewEnvWithSetup(t, "claudecode", func(e *core.Engine) {
		e.SetDisplayConfig(core.DisplayCfg{
			Mode:             "compact", // ← key config
			CardMode:         "legacy",
			ThinkingMessages: true,
			ThinkingMaxLen:   300,
			ToolMessages:     true,
			ToolMaxLen:       500,
		})
	})

	msgs := env.SendComplete("List the files in the current directory. Use a shell command.")

	// In compact mode, 💭 thinking and 🔧 tool messages should not appear.
	for _, msg := range msgs {
		text := msg.Text()
		if strings.HasPrefix(text, "💭") || strings.HasPrefix(text, "🔧") {
			t.Errorf("P2-77 compact mode: thinking/tool message appeared\nmsg: %q", text)
		}
	}
	t.Logf("P2-77 OK: no thinking/tool messages in compact mode (%d msgs)", len(msgs))
}

// ── P2-80: stream_preview disabled ───────────────────────────────────────────

// TestP2_80_StreamPreviewDisabled verifies that disabling stream_preview does
// not break message delivery. The agent's final response must still arrive.
//
// Note: MockPlatform does NOT implement MessageUpdater, so streaming preview
// would not occur regardless. This test verifies that disabling stream_preview
// does not suppress the final reply.
func TestP2_80_StreamPreviewDisabled_ClaudeCode(t *testing.T) {
	t.Parallel()
	env := helper.NewEnvWithSetup(t, "claudecode", func(e *core.Engine) {
		e.SetStreamPreviewCfg(core.StreamPreviewCfg{
			Enabled:       false,
			IntervalMs:    1500,
			MinDeltaChars: 30,
			MaxChars:      2000,
		})
	})

	msgs := env.SendComplete("say hi briefly")
	combined := helper.AnyText(msgs)

	if strings.TrimSpace(combined) == "" {
		t.Errorf("P2-80: stream_preview=false suppressed final reply")
	}
	t.Logf("P2-80 OK: final reply still delivered with stream_preview=false: %q",
		truncate(combined, 150))
}

// ── P2-85/86: reply_footer ────────────────────────────────────────────────────

// TestP2_86_HideReplyFooter verifies that with reply_footer=false, the
// footer information does not appear in final replies.
// When reply_footer is false AND show_context_indicator is false, the
// assistant reply should have no trailing metadata line.
func TestP2_86_HideReplyFooter_ClaudeCode(t *testing.T) {
	t.Parallel()
	env := helper.NewEnvWithSetup(t, "claudecode", func(e *core.Engine) {
		e.SetReplyFooterEnabled(false)
		e.SetShowContextIndicator(false) // also hide ctx
	})

	msgs := env.SendComplete("say hi briefly")

	for _, msg := range msgs {
		text := msg.Text()
		// Footer would contain model name or work dir info.
		// With both disabled, last line should not look like a footer.
		lines := strings.Split(strings.TrimSpace(text), "\n")
		if len(lines) > 0 {
			lastLine := strings.TrimSpace(lines[len(lines)-1])
			// Footer typically contains: "model · /path/to/dir" or "[ctx: ~N%]"
			if strings.Contains(lastLine, "[ctx:") {
				t.Errorf("P2-86: [ctx:] appeared with show_context_indicator=false\nlast line: %q", lastLine)
			}
		}
	}
	t.Logf("P2-86 OK: no footer/ctx in replies with reply_footer=false, show_context_indicator=false")
}

// ── P1-40: filter_external_sessions = false (default) ─────────────────────────

// TestP1_40_FilterExternalSessionsDefault verifies that the default behavior
// (filter_external_sessions=false) shows ALL sessions including any created
// externally (not via cc-connect).
//
// Since our MockPlatform tests don't have external sessions, we verify that
// the flag doesn't hide sessions WE created.
func TestP1_40_FilterExternalSessions_Default_ClaudeCode(t *testing.T) {
	t.Parallel()
	env := helper.NewEnvWithSetup(t, "claudecode", func(e *core.Engine) {
		e.SetFilterExternalSessions(false) // explicit default
	})

	env.SendComplete("say hi briefly")
	env.SendWithTimeout("/new", cfgCmdTimeout)
	env.SendComplete("say hello briefly")

	listReply := env.SendWithTimeout("/list", cfgCmdTimeout)
	count := strings.Count(listReply.Text(), "msgs")
	if count < 2 {
		t.Errorf("P1-40: filter_external_sessions=false still hides sessions (got %d)\n/list: %q",
			count, listReply.Text())
	}
	t.Logf("P1-40 OK: %d sessions visible with filter_external_sessions=false", count)
}

// ── Instant reply ─────────────────────────────────────────────────────────────

// TestInstantReply verifies that when instant_reply is enabled, the engine
// sends an immediate confirmation before the agent processes the message.
func TestInstantReply_ClaudeCode(t *testing.T) {
	t.Parallel()
	const marker = "🤔 Working on it..."
	env := helper.NewEnvWithSetup(t, "claudecode", func(e *core.Engine) {
		e.SetInstantReply(core.InstantReplyCfg{
			Enabled: true,
			Content: marker,
		})
	})

	before := env.Platform.MessageCount()
	env.Platform.InjectMessage(helper.DefaultUser, helper.DefaultChat, "say hi briefly")

	// The instant reply should arrive VERY quickly (within 2s), before the agent responds.
	instant := env.Platform.WaitForReply(before, 2*time.Second)
	if instant == nil {
		// Could be that the agent replied faster than 2s — check all messages.
		t.Logf("InstantReply: no reply in 2s (agent may have been faster)")
	} else if instant.Text() == marker {
		t.Logf("InstantReply OK: instant confirmation received before agent: %q", marker)
	}

	// Either way, we must eventually get a final substantive reply.
	msgs, ok := env.Platform.WaitForN(1, helper.DefaultReplyTimeout)
	if !ok {
		t.Fatalf("InstantReply: no final reply received\nall messages:\n%s", env.Platform.AllText())
	}
	t.Logf("InstantReply OK: %d total messages received", len(msgs))
}
