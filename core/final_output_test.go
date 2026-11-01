package core

import (
	"context"
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
	if got := finalTurnText("from result", parts, 2); got != "from result" {
		t.Fatalf("result text must win, got %q", got)
	}
	if got := finalTurnText("  ", parts, 2); got != "the answer" {
		t.Fatalf("got %q, want the last segment", got)
	}
	if got := finalTurnText("", parts, 99); got != strings.Join(parts, "") {
		t.Fatalf("an out-of-range start must fall back to all text, got %q", got)
	}
}
