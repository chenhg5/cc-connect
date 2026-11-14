package core

import (
	"strings"
	"testing"
	"time"
)

// In quiet mode, many hidden tool/thinking events between two text segments
// must leave exactly one blank line, not one per event.
func TestProcessInteractiveEvents_QuietModeAddsOneSeparatorPerGap(t *testing.T) {
	p := &cujStreamingPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetStreamPreviewCfg(DefaultStreamPreviewCfg())
	e.SetDisplayConfig(DisplayCfg{Mode: "quiet", ThinkingMaxLen: 300, ToolMaxLen: 500})

	sessionKey := "test:user1"
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := newControllableSession("s1")
	state := &interactiveState{agentSession: agentSession, platform: p, replyCtx: "ctx-1"}
	e.interactiveStates[sessionKey] = state

	go func() {
		agentSession.events <- Event{Type: EventText, Content: "on it, checking"}
		for i := 0; i < 5; i++ {
			agentSession.events <- Event{Type: EventToolUse, ToolName: "Bash", ToolInput: "pwd"}
			agentSession.events <- Event{Type: EventThinking, Content: "hmm"}
		}
		agentSession.events <- Event{Type: EventText, Content: "the answer"}
		agentSession.events <- Event{Type: EventResult, Content: "on it, checking\n\nthe answer", Done: true}
	}()
	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m1", time.Now(), nil, nil, "ctx-1", 0)

	var seen []string
	seen = append(seen, p.getPreviewOpens()...)
	for _, u := range p.getPreviewUpdates() {
		seen = append(seen, u.Content)
	}
	seen = append(seen, p.getSent()...)
	if len(seen) == 0 {
		t.Fatal("platform received no preview or message content")
	}
	for _, s := range seen {
		if strings.Contains(s, "\n\n\n") {
			t.Fatalf("content has more than one blank line in a row: %q", s)
		}
	}
}
