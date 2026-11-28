package core

import (
	"strings"
	"testing"
	"time"
)

func runQuietFinalTextTurn(t *testing.T, mode string, prepend bool, events []Event) []string {
	t.Helper()
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetDisplayConfig(DisplayCfg{Mode: mode, ToolMessages: false, PrependPreToolText: prepend})
	e.SetReplyFooterEnabled(false)
	key := "test:quiet-final-text"
	session := e.sessions.GetOrCreateActive(key)
	agentSession := newControllableSession("quiet-final-text")
	state := &interactiveState{agentSession: agentSession, platform: p, replyCtx: "reply"}
	e.interactiveStates[key] = state
	for _, event := range events {
		agentSession.events <- event
	}
	e.processInteractiveEvents(state, session, e.sessions, key, "message", time.Now(), nil, nil, state.replyCtx, 0)
	return p.getSent()
}

func TestQuietFinalTextAfterLastTool(t *testing.T) {
	sent := runQuietFinalTextTurn(t, "quiet", false, []Event{
		{Type: EventText, Content: "Checking the first thing."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "pwd"},
		{Type: EventText, Content: "Checking the second thing."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"},
		{Type: EventText, Content: "The answer is ready."},
		{Type: EventResult, Content: "The answer is ready.", Done: true},
	})
	if len(sent) == 0 {
		t.Fatal("no final reply")
	}
	final := sent[len(sent)-1]
	if strings.Contains(final, "Checking") || !strings.Contains(final, "The answer is ready.") {
		t.Fatalf("quiet final reply = %q; want only post-tool answer", final)
	}
}

func TestQuietFinalTextWithoutTool(t *testing.T) {
	sent := runQuietFinalTextTurn(t, "quiet", false, []Event{
		{Type: EventText, Content: "Direct answer."},
		{Type: EventResult, Content: "Direct answer.", Done: true},
	})
	if len(sent) == 0 || !strings.Contains(sent[len(sent)-1], "Direct answer.") {
		t.Fatalf("direct answer missing: %q", sent)
	}
}

func TestQuietFinalTextDoesNotChangeCompact(t *testing.T) {
	sent := runQuietFinalTextTurn(t, "compact", false, []Event{
		{Type: EventText, Content: "Checking."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "pwd"},
		{Type: EventText, Content: "Answer."},
		{Type: EventResult, Content: "Answer.", Done: true},
	})
	if !strings.Contains(strings.Join(sent, " "), "Checking.") {
		t.Fatalf("compact pre-tool text missing: %q", sent)
	}
}

func TestQuietFinalTextLegacyOptIn(t *testing.T) {
	sent := runQuietFinalTextTurn(t, "quiet", true, []Event{
		{Type: EventText, Content: "Checking."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "pwd"},
		{Type: EventText, Content: "Answer."},
		{Type: EventResult, Content: "Answer.", Done: true},
	})
	if len(sent) == 0 || !strings.Contains(sent[len(sent)-1], "Checking.") {
		t.Fatalf("legacy pre-tool text missing: %q", sent)
	}
}
