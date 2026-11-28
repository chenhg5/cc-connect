package core

import (
	"testing"
	"time"
)

func TestFinalAnswerModeOnRichCardPlatformUsesOnlyAnswer(t *testing.T) {
	p := &stubRichCardSilentPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	e := newFinalModeEngine(t, p)
	e.SetDisplayConfig(DisplayCfg{Mode: "final", CardMode: "rich", ToolMessages: true})
	session := runFinalModeTurn(t, e, p, "feishu:final-mode-rich-capable",
		Event{Type: EventText, Content: "Before tool. "},
		Event{Type: EventToolUse, ToolName: "Bash", ToolInput: "echo ok"},
		Event{Type: EventToolResult, ToolName: "Bash", ToolResult: "ok"},
		Event{Type: EventText, Content: "After tool."},
		Event{Type: EventResult, Content: "After tool.", Done: true},
	)
	if starts, streams, updates, deletes := p.snapshot(); len(starts)+len(streams)+len(updates)+deletes != 0 {
		t.Fatalf("final mode touched rich card: starts=%q streams=%q updates=%q deletes=%d", starts, streams, updates, deletes)
	}
	if sent := p.getSent(); len(sent) != 1 || sent[0] != "After tool." {
		t.Fatalf("sent = %q; want one plain final answer", sent)
	}
	if history := session.GetHistory(0); len(history) == 0 || history[len(history)-1].Content != "After tool." {
		t.Fatalf("history did not record only the final answer: %#v", history)
	}
}

func TestFinalAnswerModeSuppressesReportedContextWindowFooter(t *testing.T) {
	workspace := t.TempDir()
	agent := &stubReplyFooterAgent{
		stubModelModeAgent: stubModelModeAgent{model: "large-window"},
		workDir:            workspace,
	}
	p := &stubPlatformEngine{n: "telegram"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	e.SetDisplayConfig(DisplayCfg{Mode: "final"})
	e.SetReplyFooterEnabled(true)
	e.SetShowContextIndicator(true)
	sessionKey := "telegram:user-final-mode-large-window"
	session := e.sessions.GetOrCreateActive(sessionKey)
	s := newControllableSession("s-final-mode-large-window")
	s.contextUsage = &ContextUsage{UsedTokens: 190_000, ContextWindow: 1_000_000}
	state := &interactiveState{agentSession: s, platform: p, replyCtx: "ctx", agent: agent}
	e.interactiveStates[sessionKey] = state

	s.events <- Event{Type: EventResult, Content: "answer", InputTokens: 360_527, Done: true}
	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m", time.Now(), nil, nil, state.replyCtx, 0)
	if sent := p.getSent(); len(sent) != 1 || sent[0] != "answer" {
		t.Fatalf("final mode reply = %q; want only the answer and no context footer", sent)
	}
}
