package core

// Tests for issue #1302 — quiet mode "final message only" behaviour.
//
// Quiet mode's inline doc promised "final message only" but the
// implementation concatenated the pre-tool "lead-in" (e.g. "Let me check
// that for you...") directly with the post-tool answer, with no
// separator. The fix slices the accumulated text at the last tool_use
// boundary when mode == "quiet" and DisplayCfg.PrependPreToolText is
// false (the default). The pre-tool text and the "\n\n" separator
// between pre and post are dropped from the final reply.
//
// These tests run the same end-to-end event loop the production
// code uses (processInteractiveEvents), so they exercise all
// platform-agnostic finalization paths in one go: stream preview,
// sendChunksWithStatusFooter, the !isSilent branch, and the
// accumulated-textParts slice point in EventResult.

import (
	"strings"
	"testing"
	"time"
)

// runQuietTurn drives one full turn with the given display config and
// the given event sequence. It returns whatever the platform captured
// via Reply / Send. The test framework is the same
// processInteractiveEvents + controllableAgentSession path used by the
// rest of the engine tests.
func runQuietTurn(t *testing.T, cfg DisplayCfg, events []Event) []string {
	t.Helper()
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetDisplayConfig(cfg)
	e.SetReplyFooterEnabled(false) // keep final text predictable across tests

	sessionKey := "test:quiet-u1"
	session := e.sessions.GetOrCreateActive(sessionKey)
	sess := newControllableSession("s-quiet")
	state := &interactiveState{
		agentSession: sess,
		platform:     p,
		replyCtx:     "ctx-quiet",
	}
	e.interactiveStates[sessionKey] = state

	for _, ev := range events {
		sess.events <- ev
	}
	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m-quiet", time.Now(), nil, nil, state.replyCtx)
	return p.getSent()
}

// TestQuiet_Default_DropsPreToolLeadIn is the primary regression test
// for #1302. With mode = "quiet" and PrependPreToolText = false (the
// default), the user only sees the text emitted after the LAST
// tool_use. The pre-tool "Let me check that for you..." lead-in and
// the "\n\n" separator that used to glue it to the answer are both
// dropped.
func TestQuiet_Default_DropsPreToolLeadIn(t *testing.T) {
	cfg := DisplayCfg{
		Mode:               "quiet",
		ThinkingMessages:   false,
		ToolMessages:       false,
		PrependPreToolText: false,
	}
	sent := runQuietTurn(t, cfg, []Event{
		{Type: EventText, Content: "Let me check that for you."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "pwd"},
		{Type: EventText, Content: "Here is the answer: /home/user."},
		{Type: EventResult, Content: "Here is the answer: /home/user.", Done: true},
	})
	if len(sent) == 0 {
		t.Fatal("sent = nil, want at least one final reply")
	}
	// All sent messages should be the same final text (one card).
	// We just check the final text — pre-tool content must not appear.
	final := sent[len(sent)-1]
	if strings.Contains(final, "Let me check that for you") {
		t.Errorf("pre-tool lead-in leaked into final reply: %q", final)
	}
	if !strings.Contains(final, "Here is the answer: /home/user.") {
		t.Errorf("post-tool answer missing from final reply: %q", final)
	}
	if strings.Contains(final, "\n\n") {
		// The legacy bug glued the pre-tool text and post-tool text with
		// "\n\n". With the fix in place the pre-tool slice (including
		// the separator) is dropped, so the only "\n\n" we could see
		// would be inside the post-tool text itself.
		t.Errorf("unexpected \"\\n\\n\" in final reply (separator should be dropped): %q", final)
	}
}

// TestQuiet_Default_NoTool_KeepsAllText covers the no-tool case: there
// is no tool_use to mark a boundary, so the entire text is the
// "final message" and must be delivered verbatim. This guards against
// the fix accidentally regressing the common "user asks a question,
// agent answers directly" path.
func TestQuiet_Default_NoTool_KeepsAllText(t *testing.T) {
	cfg := DisplayCfg{
		Mode:               "quiet",
		ThinkingMessages:   false,
		ToolMessages:       false,
		PrependPreToolText: false,
	}
	sent := runQuietTurn(t, cfg, []Event{
		{Type: EventText, Content: "Just a direct answer."},
		{Type: EventResult, Content: "Just a direct answer.", Done: true},
	})
	if len(sent) == 0 {
		t.Fatal("sent = nil, want at least one final reply")
	}
	final := sent[len(sent)-1]
	if !strings.Contains(final, "Just a direct answer.") {
		t.Errorf("direct answer missing from final reply: %q", final)
	}
}

// TestQuiet_PrependOptIn_KeepsPreToolLeadIn verifies the opt-in path:
// with PrependPreToolText = true, the user gets the legacy
// "pre-tool + post-tool" concatenation. This is what existing
// quiet-mode users who relied on the old behaviour should set in
// their config to keep the lead-in.
//
// Note: the legacy code only inserted a "\n\n" separator between the
// pre- and post-tool slices when the platform's stream preview was
// active (`sp.canPreview()` returns true). The stub platform in this
// test does not implement stream preview, so we don't assert on the
// separator here — only that both halves of the text are present in
// the final reply, which is the user-visible behaviour the opt-in
// knob controls.
func TestQuiet_PrependOptIn_KeepsPreToolLeadIn(t *testing.T) {
	cfg := DisplayCfg{
		Mode:               "quiet",
		ThinkingMessages:   false,
		ToolMessages:       false,
		PrependPreToolText: true,
	}
	sent := runQuietTurn(t, cfg, []Event{
		{Type: EventText, Content: "Let me check that for you."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "pwd"},
		{Type: EventText, Content: "Here is the answer: /home/user."},
		{Type: EventResult, Content: "Here is the answer: /home/user.", Done: true},
	})
	if len(sent) == 0 {
		t.Fatal("sent = nil, want at least one final reply")
	}
	final := sent[len(sent)-1]
	if !strings.Contains(final, "Let me check that for you") {
		t.Errorf("pre-tool lead-in missing with PrependPreToolText=true: %q", final)
	}
	if !strings.Contains(final, "Here is the answer: /home/user.") {
		t.Errorf("post-tool answer missing with PrependPreToolText=true: %q", final)
	}
}

// TestQuiet_Default_OnlyLastToolSurfaces verifies that when multiple
// tool_uses happen, the user still only sees the text after the LAST
// one. An intermediate text block followed by another tool_use should
// also be treated as pre-tool lead-in (and dropped), because the
// final reply is anchored on the most recent tool_use.
func TestQuiet_Default_OnlyLastToolSurfaces(t *testing.T) {
	cfg := DisplayCfg{
		Mode:               "quiet",
		ThinkingMessages:   false,
		ToolMessages:       false,
		PrependPreToolText: false,
	}
	sent := runQuietTurn(t, cfg, []Event{
		{Type: EventText, Content: "First lead-in (drop me)."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"},
		{Type: EventText, Content: "Intermediate result (drop me too)."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "pwd"},
		{Type: EventText, Content: "Final answer: /home/user."},
		{Type: EventResult, Content: "Final answer: /home/user.", Done: true},
	})
	if len(sent) == 0 {
		t.Fatal("sent = nil, want at least one final reply")
	}
	final := sent[len(sent)-1]
	if strings.Contains(final, "First lead-in") {
		t.Errorf("first pre-tool lead-in leaked: %q", final)
	}
	if strings.Contains(final, "Intermediate result") {
		t.Errorf("intermediate text (between two tool_uses) leaked: %q", final)
	}
	if !strings.Contains(final, "Final answer: /home/user.") {
		t.Errorf("final answer missing: %q", final)
	}
}

// TestCompact_NotAffectedByQuietFix guards the other display modes
// against the new quiet-mode slicing. The bug only exists in quiet
// mode; compact and full mode must continue to deliver the full
// accumulated textParts in their existing forms.
func TestCompact_NotAffectedByQuietFix(t *testing.T) {
	cfg := DisplayCfg{
		Mode:               "compact",
		ThinkingMessages:   false,
		ToolMessages:       false,
		PrependPreToolText: false,
	}
	sent := runQuietTurn(t, cfg, []Event{
		{Type: EventText, Content: "Pre-tool lead-in."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "pwd"},
		{Type: EventText, Content: "Post-tool answer."},
		{Type: EventResult, Content: "Post-tool answer.", Done: true},
	})
	// Compact mode flushes each text segment as a separate card. We
	// don't assert exact message count (it's governed by compact-mode
	// segment flushing rules) — only that the pre-tool lead-in
	// appears in some sent message. The point is: the quiet-mode
	// slicing must NOT bleed into compact mode.
	found := false
	for _, s := range sent {
		if strings.Contains(s, "Pre-tool lead-in.") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("compact mode unexpectedly dropped pre-tool text: sent = %v", sent)
	}
}
