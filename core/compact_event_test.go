package core

import (
	"fmt"
	"testing"
	"time"
)

// A mid-turn context compaction reported as EventCompact neither ends the turn
// nor reaches the user: the reply produced after the compaction is delivered
// as usual and is the only thing recorded in the session history.
func TestProcessInteractiveEvents_CompactDoesNotEndTurn(t *testing.T) {
	p := &stubPlatformEngine{n: "feishu"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetDisplayConfig(DisplayCfg{Mode: "quiet", ToolMessages: false})
	sessionKey := "feishu:compact"
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := newControllableSession("compact")
	state := &interactiveState{
		agentSession: agentSession,
		platform:     p,
		replyCtx:     "ctx-compact",
	}
	e.interactiveStates[sessionKey] = state

	agentSession.events <- Event{Type: EventCompact, Metadata: map[string]any{"trigger": "auto", "pre_tokens": float64(167012)}}
	agentSession.events <- Event{Type: EventText, Content: "answer after compaction"}
	agentSession.events <- Event{Type: EventResult, Content: "answer after compaction", Done: true}

	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m-compact", time.Now(), nil, nil, state.replyCtx, 0)

	sent := p.getSent()
	want := []string{"answer after compaction"}
	if fmt.Sprint(sent) != fmt.Sprint(want) {
		t.Fatalf("sent = %#v\nwant %#v", sent, want)
	}
	if got := session.GetHistory(0); len(got) != 1 || got[0].Content != "answer after compaction" {
		t.Fatalf("history = %#v, want only the reply", got)
	}
}
