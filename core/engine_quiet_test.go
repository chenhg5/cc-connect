package core

// Tests for quiet mode "final message only" behaviour: with mode ==
// "quiet" and DisplayCfg.PrependPreToolText false (the default), the user
// sees only the text emitted after the last tool_use. The pre-tool
// "lead-in" (e.g. "Let me check that for you...") is dropped.
//
// These tests run the same end-to-end event loop the production
// code uses (processInteractiveEvents), so they exercise all
// platform-agnostic finalization paths in one go: stream preview,
// sendChunksWithStatusFooter, the !isSilent and isSilent branches, the
// silent-hold live-frame path, and the accumulated-textParts slice point
// in EventResult.

import (
	"context"
	"errors"
	"fmt"
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
	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m-quiet", time.Now(), nil, nil, state.replyCtx, 0)
	return p.getSent()
}

// TestQuiet_Default_DropsPreToolLeadIn: with mode = "quiet" and
// PrependPreToolText = false (the default), the user only sees the text
// emitted after the LAST tool_use. The pre-tool "Let me check that for
// you..." lead-in and any separator after it are dropped.
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
		// The pre-tool slice, separator included, is dropped, so a
		// "\n\n" here could only come from the post-tool text itself.
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
// with PrependPreToolText = true, the user gets the pre-tool and
// post-tool text together.
//
// The "\n\n" separator between the two is only inserted while the
// platform's stream preview is active. The stub platform here has no
// stream preview, so the test checks only that both halves are present.
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

// TestQuiet_StreamingCard_LiveFramesDropPreToolLeadIn checks the live card
// frames: in quiet mode the update after the last tool_use shows only the
// post-tool segment, matching the finalized card.
func TestQuiet_StreamingCard_LiveFramesDropPreToolLeadIn(t *testing.T) {
	card := &recordingStreamCard{}
	p := &recordingStreamCardPlatform{
		stubPlatformEngine: stubPlatformEngine{n: "teams"},
		card:               card,
	}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetDisplayConfig(DisplayCfg{Mode: "quiet", ThinkingMessages: false, ToolMessages: false, PrependPreToolText: false})
	e.SetReplyFooterEnabled(false)

	sessionKey := "teams:quiet-live"
	session := e.sessions.GetOrCreateActive(sessionKey)
	sess := newControllableSession("s-quiet-live")
	state := &interactiveState{agentSession: sess, platform: p, replyCtx: "ctx-quiet-live"}
	e.interactiveStates[sessionKey] = state

	for _, ev := range []Event{
		{Type: EventText, Content: "First lead-in."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"},
		{Type: EventText, Content: "Second lead-in."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "pwd"},
		{Type: EventText, Content: "Final answer."},
		{Type: EventResult, Content: "Final answer.", Done: true},
	} {
		sess.events <- ev
	}
	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m-quiet-live", time.Now(), nil, nil, state.replyCtx, 0)

	updates := card.updateBodies()
	if len(updates) == 0 {
		t.Fatal("expected at least one live card update")
	}
	// The last live frame (rendered for "Final answer.") must contain only
	// the post-last-tool segment — no earlier lead-ins.
	last := updates[len(updates)-1]
	if !strings.Contains(last, "Final answer.") {
		t.Errorf("last live frame missing the post-tool answer: %q", last)
	}
	if strings.Contains(last, "First lead-in") || strings.Contains(last, "Second lead-in") {
		t.Errorf("live card frame accumulated a pre-tool lead-in: %q", last)
	}
	// And the finalized card stays clean too (mirrors the live frame).
	if strings.Contains(card.finalContent(), "lead-in") {
		t.Errorf("finalized card leaked a lead-in: %q", card.finalContent())
	}
}

// TestQuiet_StreamingCard_PrependOptIn_LiveFramesKeepLeadIn checks that with
// PrependPreToolText the live card frames rendered after the tool call keep
// the pre-tool lead-in, and so does the finalized card.
func TestQuiet_StreamingCard_PrependOptIn_LiveFramesKeepLeadIn(t *testing.T) {
	card := &recordingStreamCard{}
	p := &recordingStreamCardPlatform{
		stubPlatformEngine: stubPlatformEngine{n: "teams"},
		card:               card,
	}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetDisplayConfig(DisplayCfg{Mode: "quiet", ThinkingMessages: false, ToolMessages: false, PrependPreToolText: true})
	e.SetReplyFooterEnabled(false)

	sessionKey := "teams:quiet-prepend-live"
	session := e.sessions.GetOrCreateActive(sessionKey)
	sess := newControllableSession("s-quiet-prepend-live")
	state := &interactiveState{agentSession: sess, platform: p, replyCtx: "ctx-quiet-prepend-live"}
	e.interactiveStates[sessionKey] = state

	for _, ev := range []Event{
		{Type: EventText, Content: "Let me check that for you."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "pwd"},
		{Type: EventText, Content: "Here is the answer: /home/user."},
		{Type: EventResult, Content: "Here is the answer: /home/user.", Done: true},
	} {
		sess.events <- ev
	}
	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m-quiet-prepend-live", time.Now(), nil, nil, state.replyCtx, 0)

	postToolFrames := 0
	for i, body := range card.updateBodies() {
		if !strings.Contains(body, "Here is the answer: /home/user.") {
			continue
		}
		postToolFrames++
		if !strings.Contains(body, "Let me check that for you.") {
			t.Errorf("live card frame %d dropped the pre-tool lead-in with PrependPreToolText=true: %q", i, body)
		}
	}
	if postToolFrames == 0 {
		t.Fatalf("no live card frame rendered the post-tool answer: %q", card.updateBodies())
	}
	final := card.finalContent()
	if !strings.Contains(final, "Let me check that for you.") || !strings.Contains(final, "Here is the answer: /home/user.") {
		t.Errorf("finalized card missing lead-in or answer with PrependPreToolText=true: %q", final)
	}
}

// TestQuiet_StreamingCard_SilentAfterToolNoMarkerFlash covers a lead-in, a
// tool call, then a bare NO_REPLY: neither the live card frames nor the
// finalized card may render the marker or the lead-in.
func TestQuiet_StreamingCard_SilentAfterToolNoMarkerFlash(t *testing.T) {
	card := &recordingStreamCard{}
	p := &recordingStreamCardPlatform{
		stubPlatformEngine: stubPlatformEngine{n: "teams"},
		card:               card,
	}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetDisplayConfig(DisplayCfg{Mode: "quiet", ThinkingMessages: false, ToolMessages: false, PrependPreToolText: false})
	e.SetReplyFooterEnabled(false)

	sessionKey := "teams:quiet-silent"
	session := e.sessions.GetOrCreateActive(sessionKey)
	sess := newControllableSession("s-quiet-silent")
	state := &interactiveState{agentSession: sess, platform: p, replyCtx: "ctx-quiet-silent"}
	e.interactiveStates[sessionKey] = state

	for _, ev := range []Event{
		{Type: EventText, Content: "Working on it."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"},
		{Type: EventText, Content: "NO_REPLY"},
		{Type: EventResult, Content: "NO_REPLY", Done: true},
	} {
		sess.events <- ev
	}
	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m-quiet-silent", time.Now(), nil, nil, state.replyCtx, 0)

	// No live frame may render the raw NO_REPLY marker.
	for i, body := range card.updateBodies() {
		if strings.Contains(body, "NO_REPLY") {
			t.Errorf("live card frame %d flashed the NO_REPLY marker: %q", i, body)
		}
	}
	// The finalized card must not render the marker either.
	if strings.Contains(card.finalContent(), "NO_REPLY") {
		t.Errorf("finalized card leaked the NO_REPLY marker: %q", card.finalContent())
	}
	if strings.Contains(card.finalContent(), "Working on it") {
		t.Errorf("finalized silent card leaked the pre-tool lead-in: %q", card.finalContent())
	}
}

// TestQuiet_QueuedTurnAfterTool_NoPanic runs a queued quiet turn after a turn
// that used a tool. The queued turn starts with empty textParts, so a tool
// boundary left over from the first turn would slice out of range and panic
// the event loop.
func TestQuiet_QueuedTurnAfterTool_NoPanic(t *testing.T) {
	card := &recordingStreamCard{}
	p := &recordingStreamCardPlatform{
		stubPlatformEngine: stubPlatformEngine{n: "teams"},
		card:               card,
	}
	sess := newQueuingSession("qs-quiet-panic")
	agent := &controllableAgent{nextSession: sess}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.SetDisplayConfig(DisplayCfg{Mode: "quiet", ThinkingMessages: false, ToolMessages: false, PrependPreToolText: false})
	e.SetReplyFooterEnabled(false)

	key := "teams:quiet-queued"
	session := e.sessions.GetOrCreateActive(key)
	state := &interactiveState{
		agentSession: sess,
		platform:     p,
		replyCtx:     "ctx-turn1",
		pendingMessages: []queuedMessage{
			{platform: p, replyCtx: "ctx-turn2", content: "queued-msg"},
		},
	}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()

	go func() {
		// Turn 1: text then a tool_use (sets postLastToolStart) then result.
		sess.events <- Event{Type: EventText, Content: "Checking..."}
		sess.events <- Event{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"}
		sess.events <- Event{Type: EventResult, Content: "done", Done: true}
		// Wait for the queued message's Send() before pushing turn 2 events.
		sess.sendMu.Lock()
		for len(sess.sendCalls) == 0 {
			sess.sendMu.Unlock()
			time.Sleep(5 * time.Millisecond)
			sess.sendMu.Lock()
		}
		sess.sendMu.Unlock()
		// Turn 2: its first EventText slices textParts at the tool boundary.
		sess.events <- Event{Type: EventText, Content: "Hello again"}
		sess.events <- Event{Type: EventResult, Content: "Hello again", Done: true}
	}()

	session.AddHistory("user", "initial-msg")
	sendDone := make(chan error, 1)
	sendDone <- nil

	done := make(chan struct{})
	panicked := make(chan any, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				panicked <- r
			}
			close(done)
		}()
		e.processInteractiveEvents(state, session, e.sessions, key, "msg1", time.Now(), nil, sendDone, nil, 0)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("processInteractiveEvents did not complete in time")
	}
	select {
	case r := <-panicked:
		t.Fatalf("queued quiet turn after a tool panicked: %v", r)
	default:
	}
}

// TestQuiet_HookRejectedAfterTool_DeliversRewrite covers a Stop-hook rejection
// of a draft written after a tool call. The rejection empties textParts, so a
// tool boundary left over from before it would slice out of range and panic
// the event loop.
func TestQuiet_HookRejectedAfterTool_DeliversRewrite(t *testing.T) {
	cfg := DisplayCfg{Mode: "quiet", ToolMessages: false}
	var sent []string
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("hook rejection after a tool panicked: %v", r)
			}
		}()
		sent = runQuietTurn(t, cfg, []Event{
			{Type: EventText, Content: "Let me check."},
			{Type: EventToolUse, ToolName: "Bash", ToolInput: "pwd"},
			{Type: EventText, Content: "Draft answer."},
			{Type: EventHookRejected},
			{Type: EventText, Content: "Rewritten answer."},
			{Type: EventResult, Content: "Rewritten answer.", Done: true},
		})
	}()
	if len(sent) != 1 || sent[0] != "Rewritten answer." {
		t.Fatalf("final reply = %#v, want only the rewritten answer %q", sent, "Rewritten answer.")
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

// runQuietEvents drives one turn on platform p and returns the session so
// tests can read the history entry the turn recorded. Stream preview runs
// unthrottled so every chunk reaches the platform.
func runQuietEvents(t *testing.T, p Platform, cfg DisplayCfg, events []Event) *Session {
	t.Helper()
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetDisplayConfig(cfg)
	e.SetReplyFooterEnabled(false)
	e.SetStreamPreviewCfg(StreamPreviewCfg{Enabled: true})

	sessionKey := p.Name() + ":quiet-events"
	session := e.sessions.GetOrCreateActive(sessionKey)
	sess := newControllableSession("s-quiet-events")
	state := &interactiveState{agentSession: sess, platform: p, replyCtx: "ctx-quiet-events"}
	e.interactiveStates[sessionKey] = state

	for _, ev := range events {
		sess.events <- ev
	}
	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m-quiet-events", time.Now(), nil, nil, state.replyCtx, 0)
	return session
}

func lastAssistantHistory(t *testing.T, s *Session) string {
	t.Helper()
	h := s.GetHistory(0)
	for i := len(h) - 1; i >= 0; i-- {
		if h[i].Role == "assistant" {
			return h[i].Content
		}
	}
	t.Fatalf("no assistant history entry in %#v", h)
	return ""
}

var quietDefault = DisplayCfg{Mode: "quiet", ThinkingMessages: false, ToolMessages: false, PrependPreToolText: false}

// A turn that ends on a tool call with no text after it still delivers the
// last thing the agent said.
func TestQuiet_EmptyTail_FallsBackToLastTextBlock(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	session := runQuietEvents(t, p, quietDefault, []Event{
		{Type: EventText, Content: "Done, committed."},
		{Type: EventToolUse, ToolName: "TodoWrite", ToolInput: "{}"},
		{Type: EventResult, Done: true},
	})
	sent := p.getSent()
	if len(sent) != 1 || sent[0] != "Done, committed." {
		t.Fatalf("sent = %#v, want only %q", sent, "Done, committed.")
	}
	if got := lastAssistantHistory(t, session); got != "Done, committed." {
		t.Errorf("history = %q, want %q", got, "Done, committed.")
	}
}

// With several tool calls and nothing after the last one, the fallback is
// the text between the last two tool calls, not every earlier block.
func TestQuiet_EmptyTail_UsesMostRecentBlock(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	runQuietEvents(t, p, quietDefault, []Event{
		{Type: EventText, Content: "First."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"},
		{Type: EventText, Content: "Second."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "pwd"},
		{Type: EventResult, Done: true},
	})
	sent := p.getSent()
	if len(sent) != 1 || sent[0] != "Second." {
		t.Fatalf("sent = %#v, want only %q", sent, "Second.")
	}
}

// A thinking event between the tool call and the answer must not put the
// preview separator at the start of the delivered reply.
func TestQuiet_ThinkingAfterTool_NoLeadingSeparator(t *testing.T) {
	p := &cujStreamingPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	session := runQuietEvents(t, p, quietDefault, []Event{
		{Type: EventText, Content: "Let me look."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"},
		{Type: EventThinking, Content: "the listing shows two files"},
		{Type: EventText, Content: "Answer."},
		{Type: EventResult, Content: "Answer.", Done: true},
	})
	if got := lastAssistantHistory(t, session); got != "Answer." {
		t.Errorf("history = %q, want %q", got, "Answer.")
	}
	updates := p.getPreviewUpdates()
	if len(updates) == 0 {
		t.Fatal("expected the preview to be finalized with the answer")
	}
	if final := updates[len(updates)-1].Content; final != "Answer." {
		t.Errorf("final preview = %q, want %q", final, "Answer.")
	}
}

// text -> tool -> NO_REPLY stays silent, and history records the post-tool
// marker, not the lead-in glued to it.
func TestQuiet_SilentAfterTool_HistoryKeepsMarker(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	session := runQuietEvents(t, p, quietDefault, []Event{
		{Type: EventText, Content: "Working on it."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"},
		{Type: EventText, Content: "NO_REPLY"},
		{Type: EventResult, Content: "NO_REPLY", Done: true},
	})
	if sent := p.getSent(); len(sent) != 0 {
		t.Fatalf("sent = %#v, want nothing for a silent turn", sent)
	}
	if got := lastAssistantHistory(t, session); got != "NO_REPLY" {
		t.Errorf("history = %q, want %q", got, "NO_REPLY")
	}
}

// With tool messages shown, each segment is already its own message, so
// quiet mode keeps the pre-tool text on the live card.
func TestQuiet_ToolMessagesShown_LiveCardKeepsLeadIn(t *testing.T) {
	card := &recordingStreamCard{}
	p := &recordingStreamCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "teams"}, card: card}
	cfg := quietDefault
	cfg.ToolMessages = true
	runQuietEvents(t, p, cfg, []Event{
		{Type: EventText, Content: "Let me look."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"},
		{Type: EventText, Content: "Answer."},
		{Type: EventResult, Content: "Answer.", Done: true},
	})
	updates := card.updateBodies()
	if len(updates) == 0 {
		t.Fatal("expected live card updates")
	}
	if last := updates[len(updates)-1]; !strings.Contains(last, "Let me look.") {
		t.Errorf("last live frame = %q, want it to keep the lead-in", last)
	}
}

var quietRich = DisplayCfg{Mode: "quiet", CardMode: "rich", ThinkingMessages: false, ToolMessages: false, PrependPreToolText: false}

func newQuietRichPlatform() *stubRichCardSilentPlatform {
	return &stubRichCardSilentPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
}

// richFrames returns every card body the platform received, in send order
// per channel: card creations, streamed text, then full-card updates.
func richFrames(p *stubRichCardSilentPlatform) []string {
	starts, streams, updates, _ := p.snapshot()
	return append(append(starts, streams...), updates...)
}

func TestQuiet_RichCard_NormalFinalDropsLeadIn(t *testing.T) {
	p := newQuietRichPlatform()
	session := runQuietEvents(t, p, quietRich, []Event{
		{Type: EventText, Content: "Let me inspect first."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"},
		{Type: EventText, Content: "Final answer."},
		{Type: EventResult, Content: "Final answer.", Done: true},
	})
	_, _, updates, deletes := p.snapshot()
	if deletes != 0 {
		t.Errorf("card deleted %d times, want it finalized in place", deletes)
	}
	if len(updates) == 0 {
		t.Fatal("expected the card to be finalized")
	}
	if final := updates[len(updates)-1]; !strings.Contains(final, `status=done`) || !strings.Contains(final, `body="Final answer."`) {
		t.Errorf("final card = %q, want status done with body %q", final, "Final answer.")
	}
	if got := lastAssistantHistory(t, session); got != "Final answer." {
		t.Errorf("history = %q, want %q", got, "Final answer.")
	}
}

func TestQuiet_RichCard_SilentFinalDeletesCard(t *testing.T) {
	p := newQuietRichPlatform()
	session := runQuietEvents(t, p, quietRich, []Event{
		{Type: EventText, Content: "Working on it."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"},
		{Type: EventText, Content: "NO_REPLY"},
		{Type: EventResult, Content: "NO_REPLY", Done: true},
	})
	if sent := p.getSent(); len(sent) != 0 {
		t.Errorf("sent = %#v, want nothing for a silent turn", sent)
	}
	if _, _, _, deletes := p.snapshot(); deletes != 1 {
		t.Errorf("card deleted %d times, want 1: the card has nothing left to show", deletes)
	}
	for i, f := range richFrames(p) {
		if strings.Contains(f, "NO_REPLY") {
			t.Errorf("frame %d rendered the NO_REPLY marker: %q", i, f)
		}
	}
	if got := lastAssistantHistory(t, session); got != "NO_REPLY" {
		t.Errorf("history = %q, want %q", got, "NO_REPLY")
	}
}

// The rich card drops the lead-in as soon as the tool starts, and no later
// frame brings it back.
func TestQuiet_RichCard_ToolBoundaryClearsLeadIn(t *testing.T) {
	p := newQuietRichPlatform()
	runQuietEvents(t, p, quietRich, []Event{
		{Type: EventText, Content: "Let me inspect first."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"},
		{Type: EventText, Content: "Final answer, which is long enough to stream."},
		{Type: EventResult, Content: "Final answer, which is long enough to stream.", Done: true},
	})
	_, streams, updates, _ := p.snapshot()
	if len(updates) == 0 || !strings.Contains(updates[0], `body=""`) {
		t.Fatalf("first card update = %q, want the tool boundary to clear the body", updates)
	}
	for i, f := range append(streams, updates...) {
		if strings.Contains(f, "Final answer") && strings.Contains(f, "Let me inspect") {
			t.Errorf("frame %d shows the lead-in next to the answer: %q", i, f)
		}
	}
}

func TestQuiet_RichCard_ToolFirstCreatesCardAtText(t *testing.T) {
	p := newQuietRichPlatform()
	runQuietEvents(t, p, quietRich, []Event{
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"},
		{Type: EventText, Content: "Answer."},
		{Type: EventResult, Content: "Answer.", Done: true},
	})
	starts, _, updates, _ := p.snapshot()
	if len(starts) != 1 {
		t.Errorf("card creations = %q, want one card, created by the text and not the tool call", starts)
	}
	if len(updates) == 0 || !strings.Contains(updates[len(updates)-1], `body="Answer."`) {
		t.Errorf("final card = %q, want body %q", updates, "Answer.")
	}
}

func TestQuiet_RichCard_PrependOptInKeepsLeadIn(t *testing.T) {
	p := newQuietRichPlatform()
	cfg := quietRich
	cfg.PrependPreToolText = true
	runQuietEvents(t, p, cfg, []Event{
		{Type: EventText, Content: "Let me inspect first."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"},
		{Type: EventText, Content: "Final answer."},
		{Type: EventResult, Content: "Final answer.", Done: true},
	})
	_, _, updates, _ := p.snapshot()
	if len(updates) == 0 {
		t.Fatal("expected the card to be finalized")
	}
	final := updates[len(updates)-1]
	if !strings.Contains(final, "Let me inspect first.") || !strings.Contains(final, "Final answer.") {
		t.Errorf("final card = %q, want lead-in and answer with PrependPreToolText", final)
	}
}

func TestQuiet_RichCard_ErrorAfterToolDropsLeadIn(t *testing.T) {
	p := newQuietRichPlatform()
	runQuietEvents(t, p, quietRich, []Event{
		{Type: EventText, Content: "Let me inspect first."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"},
		{Type: EventError, Error: errors.New("boom")},
	})
	_, _, updates, _ := p.snapshot()
	if len(updates) == 0 {
		t.Fatal("expected an error card")
	}
	if last := updates[len(updates)-1]; !strings.Contains(last, "status=error") || strings.Contains(last, "Let me inspect") {
		t.Errorf("error card = %q, want status error without the lead-in", last)
	}
}

func previewFrames(p *cujStreamingPlatform) []string {
	frames := p.getPreviewOpens()
	for _, u := range p.getPreviewUpdates() {
		frames = append(frames, u.Content)
	}
	return frames
}

// A thinking frame rendered after the answer started must not bring the
// pre-tool text back onto the streaming card.
func TestQuiet_StreamingCard_ThinkingFrameKeepsLeadInOut(t *testing.T) {
	card := &recordingStreamCard{}
	p := &recordingStreamCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "teams"}, card: card}
	cfg := quietDefault
	cfg.ThinkingMessages = true
	runQuietEvents(t, p, cfg, []Event{
		{Type: EventText, Content: "Let me look."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"},
		{Type: EventText, Content: "Answer."},
		{Type: EventThinking, Content: "double-checking"},
		{Type: EventResult, Content: "Answer.", Done: true},
	})
	answerShown := false
	for i, body := range card.updateBodies() {
		if body == "" {
			t.Errorf("frame %d is empty; the card should keep its last frame", i)
		}
		if strings.Contains(body, "Answer.") {
			answerShown = true
		}
		if answerShown && strings.Contains(body, "Let me look.") {
			t.Errorf("frame %d shows the lead-in after the answer started: %q", i, body)
		}
	}
	if !answerShown {
		t.Fatalf("no frame showed the answer: %q", card.updateBodies())
	}
}

// The plain preview keeps the lead-in until the first post-tool text, which
// replaces it.
func TestQuiet_Preview_FirstPostToolChunkReplacesLeadIn(t *testing.T) {
	p := &cujStreamingPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	runQuietEvents(t, p, quietDefault, []Event{
		{Type: EventText, Content: "Let me look."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"},
		{Type: EventText, Content: "Answer."},
		{Type: EventResult, Content: "Answer.", Done: true},
	})
	frames := previewFrames(p)
	if len(frames) == 0 || frames[0] != "Let me look." {
		t.Fatalf("preview frames = %q, want the lead-in shown first", frames)
	}
	for i, f := range frames {
		if strings.Contains(f, "Answer.") && strings.Contains(f, "Let me look.") {
			t.Errorf("frame %d shows the lead-in next to the answer: %q", i, f)
		}
	}
}

func TestQuiet_Preview_PrependOptInKeepsLeadIn(t *testing.T) {
	p := &cujStreamingPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	cfg := quietDefault
	cfg.PrependPreToolText = true
	runQuietEvents(t, p, cfg, []Event{
		{Type: EventText, Content: "Let me look."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"},
		{Type: EventText, Content: "Answer."},
		{Type: EventResult, Content: "Answer.", Done: true},
	})
	frames := previewFrames(p)
	if last := frames[len(frames)-1]; !strings.Contains(last, "Let me look.") || !strings.Contains(last, "Answer.") {
		t.Errorf("final preview = %q, want lead-in and answer with PrependPreToolText", last)
	}
}

// cleaningPreviewPlatform is a stream-preview platform that can delete
// previews, and records the handles it deleted.
type cleaningPreviewPlatform struct {
	cujStreamingPlatform
	deleted []any
}

func (p *cleaningPreviewPlatform) DeletePreviewMessage(_ context.Context, handle any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.deleted = append(p.deleted, handle)
	return nil
}

func (p *cleaningPreviewPlatform) wasDeleted(handle any) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, h := range p.deleted {
		if h == handle {
			return true
		}
	}
	return false
}

// runQuietPermissionTurn drives text -> tool -> permission -> text -> tool ->
// text -> result on platform p, approving the prompt as soon as it shows.
func runQuietPermissionTurn(t *testing.T, p Platform) *Session {
	t.Helper()
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetDisplayConfig(quietDefault)
	e.SetReplyFooterEnabled(false)
	e.SetStreamPreviewCfg(StreamPreviewCfg{Enabled: true})

	sessionKey := "test:quiet-permission"
	session := e.sessions.GetOrCreateActive(sessionKey)
	sess := newControllableSession("s-quiet-permission")
	state := &interactiveState{agentSession: sess, platform: p, replyCtx: "ctx-quiet-permission"}
	e.interactiveStates[sessionKey] = state

	go func() {
		for i := 0; i < 200; i++ {
			state.mu.Lock()
			pd := state.pending
			state.mu.Unlock()
			if pd != nil {
				close(pd.Resolved)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	for _, ev := range []Event{
		{Type: EventText, Content: "Lead."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"},
		{Type: EventPermissionRequest, RequestID: "r1", ToolName: "Bash", ToolInput: "ls"},
		{Type: EventText, Content: "Mid."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "pwd"},
		{Type: EventText, Content: "Final."},
		{Type: EventResult, Content: "Final.", Done: true},
	} {
		sess.events <- ev
	}
	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m-quiet-permission", time.Now(), nil, nil, state.replyCtx, 0)
	return session
}

func assertFinalReply(t *testing.T, sent []string, want string) {
	t.Helper()
	if len(sent) == 0 || sent[len(sent)-1] != want {
		t.Fatalf("sent = %q, want the final reply %q last", sent, want)
	}
}

// A permission prompt sends no text before it. The live preview of the
// lead-in is deleted, its blocks stay apart, and the final reply is the text
// after the last tool call.
func TestQuiet_PermissionPromptKeepsBlocksApart(t *testing.T) {
	p := &cleaningPreviewPlatform{cujStreamingPlatform: cujStreamingPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}}
	runQuietPermissionTurn(t, p)

	for i, s := range append(p.getSent(), previewFrames(&p.cujStreamingPlatform)...) {
		if strings.Contains(s, "Mid.Final.") {
			t.Errorf("message %d glues two text blocks together: %q", i, s)
		}
	}
	for i, s := range p.getSent() {
		if strings.Contains(s, "Lead.") {
			t.Errorf("sent message %d shows the lead-in: %q", i, s)
		}
	}
	for i, open := range p.getPreviewOpens() {
		if handle := fmt.Sprintf("preview-%d", i+1); strings.Contains(open, "Lead.") && !p.wasDeleted(handle) {
			t.Errorf("preview %s shows the lead-in and was not deleted", handle)
		}
	}
	for i, u := range p.getPreviewUpdates() {
		if strings.Contains(u.Content, "Lead.") {
			t.Errorf("preview update %d shows the lead-in: %q", i, u.Content)
		}
	}
	assertFinalReply(t, p.getSent(), "Final.")
}

// Without a stream preview, a permission prompt sends no text before it and
// the final reply is the text after the last tool call.
func TestQuiet_PermissionPromptNoPreview_SendsOnlyFinal(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	runQuietPermissionTurn(t, p)

	for i, s := range p.getSent() {
		if strings.Contains(s, "Lead.") || strings.Contains(s, "Mid.") {
			t.Errorf("sent message %d shows text before the last tool call: %q", i, s)
		}
	}
	assertFinalReply(t, p.getSent(), "Final.")
}

// A NO_REPLY result after the last tool call keeps the turn silent, even
// though no text followed the tool call.
func TestQuiet_SilentResultAfterTool_StaysSilent(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	session := runQuietEvents(t, p, quietDefault, []Event{
		{Type: EventText, Content: "LEADIN."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"},
		{Type: EventResult, Content: "NO_REPLY", Done: true},
	})
	if sent := p.getSent(); len(sent) != 0 {
		t.Fatalf("sent = %#v, want nothing for a silent turn", sent)
	}
	if got := lastAssistantHistory(t, session); got != "NO_REPLY" {
		t.Errorf("history = %q, want %q", got, "NO_REPLY")
	}
}

// closingSession ends the event stream without a result event, as an agent
// process that exits mid-turn does.
type closingSession struct {
	*controllableAgentSession
	ch chan Event
}

func (s *closingSession) Events() <-chan Event { return s.ch }
func (s *closingSession) Close() error         { return nil }

// An agent that exits after text -> tool -> NO_REPLY must not show the marker.
func TestQuiet_AgentExitAfterToolHidesMarker(t *testing.T) {
	p := &cujStreamingPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetDisplayConfig(quietDefault)
	e.SetReplyFooterEnabled(false)
	e.SetStreamPreviewCfg(StreamPreviewCfg{Enabled: true})

	sessionKey := "test:quiet-exit"
	session := e.sessions.GetOrCreateActive(sessionKey)
	sess := &closingSession{controllableAgentSession: newControllableSession("s-quiet-exit"), ch: make(chan Event, 8)}
	state := &interactiveState{agentSession: sess, platform: p, replyCtx: "ctx-quiet-exit"}
	e.interactiveStates[sessionKey] = state

	for _, ev := range []Event{
		{Type: EventText, Content: "Working."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"},
		{Type: EventText, Content: "NO_REPLY"},
	} {
		sess.ch <- ev
	}
	close(sess.ch)
	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m-quiet-exit", time.Now(), nil, nil, state.replyCtx, 0)

	for i, s := range append(p.getSent(), previewFrames(p)...) {
		if strings.Contains(s, "NO_REPLY") {
			t.Errorf("message %d shows the NO_REPLY marker: %q", i, s)
		}
	}
}

// An agent that exits after text -> tool -> text delivers only the text
// after the tool call.
func TestQuiet_AgentExitAfterTool_DeliversPostToolText(t *testing.T) {
	p := &cujStreamingPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetDisplayConfig(quietDefault)
	e.SetReplyFooterEnabled(false)
	e.SetStreamPreviewCfg(StreamPreviewCfg{Enabled: true})

	sessionKey := "test:quiet-exit-final"
	session := e.sessions.GetOrCreateActive(sessionKey)
	sess := &closingSession{controllableAgentSession: newControllableSession("s-quiet-exit-final"), ch: make(chan Event, 8)}
	state := &interactiveState{agentSession: sess, platform: p, replyCtx: "ctx-quiet-exit-final"}
	e.interactiveStates[sessionKey] = state

	for _, ev := range []Event{
		{Type: EventText, Content: "Lead."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"},
		{Type: EventText, Content: "Final."},
	} {
		sess.ch <- ev
	}
	close(sess.ch)
	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m-quiet-exit-final", time.Now(), nil, nil, state.replyCtx, 0)

	frames := previewFrames(p)
	if len(frames) == 0 || frames[len(frames)-1] != "Final." {
		t.Fatalf("preview frames = %q, want %q last", frames, "Final.")
	}
	if sent := p.getSent(); len(sent) != 0 {
		t.Errorf("sent = %q, want the reply delivered through the preview only", sent)
	}
	if got := lastAssistantHistory(t, session); got != "Final." {
		t.Errorf("history = %q, want %q", got, "Final.")
	}
}
