package core

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// finalModePlatform offers everything a turn could decorate its output
// with: progress cards, a streaming preview and a status footer.
type finalModePlatform struct {
	stubCompactProgressPlatform
	extraMu     sync.Mutex
	footerCalls []string
}

func (p *finalModePlatform) SendWithStatusFooter(_ context.Context, _ any, content, footer string) error {
	p.extraMu.Lock()
	p.footerCalls = append(p.footerCalls, content+"|FOOTER|"+footer)
	p.extraMu.Unlock()
	return nil
}

func newFinalModePlatform() *finalModePlatform {
	return &finalModePlatform{stubCompactProgressPlatform: stubCompactProgressPlatform{
		stubPlatformEngine: stubPlatformEngine{n: "test"}, style: "card", supportPayload: true,
	}}
}

func newFinalModeEngine(t *testing.T, p Platform) *Engine {
	t.Helper()
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	// Thinking/tool flags are forced off by final mode whatever is passed.
	e.SetDisplayConfig(DisplayCfg{Mode: "final", ThinkingMessages: true, ToolMessages: true})
	e.SetReplyFooterEnabled(true)
	e.SetShowContextIndicator(true)
	e.SetInstantReply(InstantReplyCfg{Enabled: true, Content: "working"})
	return e
}

func runFinalModeTurn(t *testing.T, e *Engine, p Platform, key string, events ...Event) *Session {
	t.Helper()
	session := e.sessions.GetOrCreateActive(key)
	s := newControllableSession("final")
	s.events = make(chan Event, len(events)+1)
	for _, ev := range events {
		s.events <- ev
	}
	state := &interactiveState{agentSession: s, platform: p, replyCtx: "ctx"}
	done := make(chan struct{})
	go func() {
		e.processInteractiveEvents(state, session, e.sessions, key, "m", time.Now(), nil, nil, "ctx", 0)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("turn did not complete")
	}
	return session
}

// In final mode a turn sends one plain message: its final answer. Nothing
// else -- no instant reply, progress card, preview, thinking, tool message,
// narration before the tool call or footer.
func TestProcessInteractiveEvents_FinalModeSendsOnlyTheFinalText(t *testing.T) {
	p := newFinalModePlatform()
	e := newFinalModeEngine(t, p)
	final := "Two open items: #7 and #8."
	session := runFinalModeTurn(t, e, p, "test:final",
		Event{Type: EventThinking, Content: "planning the answer"},
		Event{Type: EventText, Content: "Let me look at the board."},
		Event{Type: EventToolUse, ToolName: "Bash", ToolInput: "cat board.md"},
		Event{Type: EventToolResult, ToolName: "Bash", ToolResult: "ok"},
		Event{Type: EventText, Content: final},
		Event{Type: EventResult, Content: final, Done: true, InputTokens: 5000},
	)

	sent := p.getSent()
	if len(sent) != 1 || sent[0] != final {
		t.Fatalf("sent = %q, want only the final answer", sent)
	}
	if starts, edits := p.getPreviewStarts(), p.getPreviewEdits(); len(starts) != 0 || len(edits) != 0 {
		t.Fatalf("final mode drew cards/previews: starts=%q edits=%q", starts, edits)
	}
	p.extraMu.Lock()
	footers := p.footerCalls
	p.extraMu.Unlock()
	if len(footers) != 0 {
		t.Fatalf("final mode added a footer: %q", footers)
	}
	// History keeps what the agent said.
	history := session.GetHistory(0)
	if len(history) == 0 || history[len(history)-1].Content != final {
		t.Fatalf("history = %+v", history)
	}
}

// Without text on the result event the last text segment is the answer;
// narration before a tool call or thinking block is not.
func TestProcessInteractiveEvents_FinalModeUsesTheLastSegment(t *testing.T) {
	p := newFinalModePlatform()
	e := newFinalModeEngine(t, p)
	runFinalModeTurn(t, e, p, "test:final-segment",
		Event{Type: EventText, Content: "Let me check."},
		Event{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"},
		Event{Type: EventText, Content: "Hmm, one more look."},
		Event{Type: EventThinking, Content: "count them"},
		Event{Type: EventText, Content: "Two "},
		Event{Type: EventText, Content: "items."},
		Event{Type: EventResult, Done: true},
	)
	if sent := p.getSent(); len(sent) != 1 || sent[0] != "Two items." {
		t.Fatalf("sent = %q, want only the last segment", sent)
	}
}

func TestProcessInteractiveEvents_FinalModeNoReplyIsSilent(t *testing.T) {
	p := newFinalModePlatform()
	e := newFinalModeEngine(t, p)
	runFinalModeTurn(t, e, p, "test:final-silent",
		Event{Type: EventToolUse, ToolName: "Bash", ToolInput: "./check.sh"},
		Event{Type: EventText, Content: "NO_REPLY"},
		Event{Type: EventResult, Content: "NO_REPLY", Done: true},
	)
	if sent := p.getSent(); len(sent) != 0 {
		t.Fatalf("NO_REPLY sent %q", sent)
	}
}

// A streaming-card platform gets no card in final mode; the answer is sent.
func TestProcessInteractiveEvents_FinalModeSkipsStreamingCards(t *testing.T) {
	card := &recordingStreamCard{}
	p := &recordingStreamCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "slack"}, card: card}
	e := newFinalModeEngine(t, p)
	runFinalModeTurn(t, e, p, "slack:final-stream",
		Event{Type: EventThinking, Content: "hmm"},
		Event{Type: EventText, Content: "done"},
		Event{Type: EventResult, Content: "done", Done: true},
	)
	if card.finalized() {
		t.Fatal("final mode used the streaming card")
	}
	if sent := p.getSent(); len(sent) != 1 || sent[0] != "done" {
		t.Fatalf("sent = %q", sent)
	}
}

// /quiet and /config mode cannot leave final mode; /quiet says so.
func TestCmdQuiet_FinalModeIsFixed(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	e.SetDisplayConfig(DisplayCfg{Mode: "final"})
	saved := false
	e.SetDisplaySaveFunc(func(*string, *bool, *int, *int, *bool) error { saved = true; return nil })
	for _, args := range [][]string{nil, {"full"}} {
		e.cmdQuiet(p, &Message{SessionKey: "test:q", ReplyCtx: "ctx"}, args)
	}
	if e.display.Mode != "final" || e.display.ThinkingMessages || e.display.ToolMessages || saved {
		t.Fatalf("display changed: %+v saved=%t", e.display, saved)
	}
	sent := p.getSent()
	if len(sent) != 2 || !strings.Contains(sent[0], "final") {
		t.Fatalf("replies = %q, want an explanation naming final mode", sent)
	}
	var modeItem *configItem
	for i, item := range e.configItems() {
		if item.key == "mode" {
			modeItem = &e.configItems()[i]
		}
	}
	if modeItem == nil {
		t.Fatal("no mode config item")
	}
	if err := modeItem.setFunc("full"); err == nil || e.display.Mode != "final" || saved {
		t.Fatalf("/config mode left final mode: err=%v mode=%q saved=%t", err, e.display.Mode, saved)
	}
}

// System notices (auto-compress, resume and startup warnings, model
// switches) are not chat output in final mode; other modes keep them.
func TestFinalMode_SystemNoticesAreNotSent(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	e.SetDisplayConfig(DisplayCfg{Mode: "final"})
	e.notice(p, "ctx", "compressing")
	if sent := p.getSent(); len(sent) != 0 {
		t.Fatalf("final mode sent notices %q", sent)
	}
	e.SetDisplayConfig(DisplayCfg{Mode: "quiet"})
	e.notice(p, "ctx", "compressing")
	if sent := p.getSent(); len(sent) != 1 {
		t.Fatalf("other modes must still send notices, got %q", sent)
	}
}

func TestFinalTurnText(t *testing.T) {
	parts := []string{"narration ", "before tool", "the ", "answer"}
	for _, tc := range []struct {
		name   string
		result string
		parts  []string
		starts []int
		want   string
	}{
		{"result text wins", "from result", parts, []int{2}, "from result"},
		{"last segment", "  ", parts, []int{2}, "the answer"},
		{"no boundary is all text", "", parts, nil, strings.Join(parts, "")},
		{"out-of-range boundary is ignored", "", parts, []int{99}, strings.Join(parts, "")},
		{"empty tail falls back to the previous segment", "", parts, []int{2, 4}, "the answer"},
		{"whitespace tail falls back", "", []string{"answer", " \n"}, []int{1}, "answer"},
		{"several empty segments are skipped", "", []string{"narration", "answer"}, []int{1, 2, 2, 2}, "answer"},
		{"no text at all", "", nil, []int{0, 0}, ""},
	} {
		if got := finalTurnText(tc.result, tc.parts, tc.starts); got != tc.want {
			t.Errorf("%s: finalTurnText = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A trailing thinking block or tool call with no text after it does not
// erase the answer: the turn falls back to the last segment that has text.
// Codex and Kimi end every turn with an EventResult without content, so for
// them the text events are the only source of the answer.
func TestProcessInteractiveEvents_FinalModeAnswerSurvivesATrailingBlock(t *testing.T) {
	answer := Event{Type: EventText, Content: "The answer is 42."}
	result := Event{Type: EventResult, Done: true}
	for _, tc := range []struct {
		name   string
		events []Event
		want   string
	}{
		{"text then end (control)", []Event{answer, result}, "The answer is 42."},
		{"text, thinking, end", []Event{answer, {Type: EventThinking, Content: "should I verify?"}, result}, "The answer is 42."},
		{"text, tool call, end", []Event{answer, {Type: EventToolUse, ToolName: "Bash", ToolInput: "true"}, result}, "The answer is 42."},
		{"text, thinking, blank text, end", []Event{answer, {Type: EventThinking, Content: "hmm"}, {Type: EventText, Content: "\n"}, result}, "The answer is 42."},
		{"narration, tool, answer, thinking, end", []Event{
			{Type: EventText, Content: "Let me check."},
			{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"},
			answer,
			{Type: EventThinking, Content: "done?"},
			result,
		}, "The answer is 42."},
		{"codex-style result without content after a tool", []Event{
			{Type: EventText, Content: "Let me check."},
			{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"},
			answer,
			{Type: EventResult, Done: true},
		}, "The answer is 42."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newFinalModePlatform()
			e := newFinalModeEngine(t, p)
			runFinalModeTurn(t, e, p, "test:final-trailing", tc.events...)
			if sent := p.getSent(); len(sent) != 1 || sent[0] != tc.want {
				t.Fatalf("sent = %q, want [%q]", sent, tc.want)
			}
		})
	}
}

// SetDisplayConfig owns the whole final-mode contract, so a DisplayCfg built
// anywhere (not only from config.EffectiveDisplay) hides the agent footer.
func TestSetDisplayConfig_FinalForcesItsInvariants(t *testing.T) {
	e := NewEngine("test", &stubAgent{}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	e.SetDisplayConfig(DisplayCfg{Mode: "final", ThinkingMessages: true, ToolMessages: true})
	if e.display.ThinkingMessages || e.display.ToolMessages || !e.display.HideAgentFooter {
		t.Fatalf("display = %+v, want thinking/tool off and the agent footer hidden", e.display)
	}
	e.SetDisplayConfig(DisplayCfg{Mode: "quiet"})
	if e.display.HideAgentFooter {
		t.Fatal("other modes must keep their own hide_agent_footer")
	}
}

// A notice whose loss costs the user something (a fresh session instead of
// a resume, a refused background tool) is logged at Warn in final mode, so
// it stays in the logs although it is not sent; other modes send it.
func TestFinalMode_WarnNoticeIsLoggedNotSent(t *testing.T) {
	buf, restore := captureSlog(t)
	defer restore()
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	e.SetDisplayConfig(DisplayCfg{Mode: "final"})
	e.warnNotice(p, "ctx", "test:warn", "started a new session")
	if sent := p.getSent(); len(sent) != 0 {
		t.Fatalf("final mode sent %q", sent)
	}
	if log := buf.String(); !strings.Contains(log, `"level":"WARN"`) || !strings.Contains(log, "started a new session") || !strings.Contains(log, "test:warn") {
		t.Fatalf("warn log = %s", log)
	}
	e.SetDisplayConfig(DisplayCfg{Mode: "full"})
	e.warnNotice(p, "ctx", "test:warn", "started a new session")
	if sent := p.getSent(); len(sent) != 1 || sent[0] != "started a new session" {
		t.Fatalf("other modes must send the notice, got %q", sent)
	}
}

// The unsafe-resume notice in final mode: not sent, but logged at Warn as a
// notice the user did not get.
func TestFinalMode_UnsafeResumeNoticeIsLogged(t *testing.T) {
	buf, restore := captureSlog(t)
	defer restore()
	agent := &controllableAgent{nextSession: newControllableSession("fresh")}
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	e.SetDisplayConfig(DisplayCfg{Mode: "final"})
	const key = "test:unsafe-resume"
	e.markUnsafeResume(key)
	e.getOrCreateInteractiveStateWith(key, p, "ctx", &Session{AgentSessionID: "old-session"}, e.sessions, nil, "")
	if sent := p.getSent(); len(sent) != 0 {
		t.Fatalf("final mode sent %q", sent)
	}
	want := e.i18n.T(MsgSessionResumeUnsafe)
	if log := buf.String(); !strings.Contains(log, "notice not sent") || !strings.Contains(log, key) || !strings.Contains(log, jsonEscape(want)) {
		t.Fatalf("warn log lacks the dropped resume notice:\n%s", log)
	}
}

// jsonEscape renders s the way slog's JSON handler writes a string value.
func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	return strings.Trim(string(b), `"`)
}

// agentStatusFooter is the model/token footer agents append to their text;
// hide_agent_footer (forced on by final mode) strips it.
const agentStatusFooter = "*claude-opus-4-8[1m] · out 788 · in 442 cw 0 cr 395.1k · ctx 40%*"

// runBackgroundTurn feeds events to a background (unsolicited) reader and
// returns what reached the chat. With result the turn ends in an
// EventResult without content; otherwise the reader is stopped, which
// flushes the buffered text.
func runBackgroundTurn(t *testing.T, display DisplayCfg, result bool, events ...Event) []string {
	t.Helper()
	p := &stubPlatformEngine{n: "test"}
	s := newControllableSession("bg-final")
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	e.SetDisplayConfig(display)
	const key = "test:bg-final"
	session := e.sessions.GetOrCreateActive(key)
	state := &interactiveState{agentSession: s, platform: p, replyCtx: "ctx"}
	e.interactiveStates[key] = state
	e.startUnsolicitedReader(state, session, e.sessions, key, "")
	for _, ev := range events {
		s.events <- ev
	}
	if result {
		s.events <- Event{Type: EventResult, Done: true}
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(s.events) > 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	e.stopUnsolicitedReader(state)
	time.Sleep(100 * time.Millisecond)
	return p.getSent()
}

// A background turn (the agent keeps working after the foreground turn
// ended) follows the final-mode contract like a foreground turn: the same
// events give the same chat output -- only the last text segment, without
// the agent's status footer -- whether the turn ends with a result or its
// buffered text is flushed.
func TestFinalMode_BackgroundTurnMatchesForeground(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events []Event
		want   string
	}{
		{"answer with a footer", []Event{{Type: EventText, Content: "The answer is 42.\n" + agentStatusFooter}}, "The answer is 42."},
		{"footer in its own chunk", []Event{{Type: EventText, Content: "The answer is 42."}, {Type: EventText, Content: "\n" + agentStatusFooter}}, "The answer is 42."},
		{"narration, tool, answer", []Event{
			{Type: EventText, Content: "Let me check."},
			{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"},
			{Type: EventText, Content: "The answer is 42."},
		}, "The answer is 42."},
		{"answer, trailing thinking", []Event{
			{Type: EventText, Content: "The answer is 42."},
			{Type: EventThinking, Content: "verify?"},
		}, "The answer is 42."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fp := newFinalModePlatform()
			fe := newFinalModeEngine(t, fp)
			runFinalModeTurn(t, fe, fp, "test:fg-final", append(append([]Event{}, tc.events...), Event{Type: EventResult, Done: true})...)
			if sent := fp.getSent(); len(sent) != 1 || sent[0] != tc.want {
				t.Fatalf("foreground sent %q, want [%q]", sent, tc.want)
			}
			for _, result := range []bool{true, false} {
				if sent := runBackgroundTurn(t, DisplayCfg{Mode: "final"}, result, tc.events...); len(sent) != 1 || sent[0] != tc.want {
					t.Errorf("background (result=%t) sent %q, want [%q] like the foreground", result, sent, tc.want)
				}
			}
		})
	}
}

// hide_agent_footer applies to background turns in every mode, and only
// when it is set.
func TestUnsolicitedReader_HideAgentFooter(t *testing.T) {
	text := Event{Type: EventText, Content: "done\n" + agentStatusFooter}
	if sent := runBackgroundTurn(t, DisplayCfg{Mode: "quiet", HideAgentFooter: true}, true, text); len(sent) != 1 || sent[0] != "done" {
		t.Fatalf("hide_agent_footer on: sent %q", sent)
	}
	if sent := runBackgroundTurn(t, DisplayCfg{Mode: "quiet"}, true, text); len(sent) != 1 || !strings.Contains(sent[0], "out 788") {
		t.Fatalf("hide_agent_footer off: sent %q, want the footer kept", sent)
	}
}
