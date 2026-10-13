package core

import (
	"context"
	"sync"
	"testing"
	"time"
)

// markingPlatform records which reply contexts its sends got, and marks agent
// output the way the Discord platform does (maomao fork).
type markingPlatform struct {
	mockKeepPreviewPlatform
	ctxMu    sync.Mutex
	previews []any
	sends    []any
}

type agentMarked struct{ inner any }

func (m *markingPlatform) MarkAgentReply(ctx any) any {
	if _, ok := ctx.(agentMarked); ok {
		return ctx
	}
	return agentMarked{ctx}
}

func (m *markingPlatform) SendPreviewStart(ctx context.Context, rctx any, content string) (any, error) {
	m.ctxMu.Lock()
	m.previews = append(m.previews, rctx)
	m.ctxMu.Unlock()
	return m.mockKeepPreviewPlatform.SendPreviewStart(ctx, rctx, content)
}

func (m *markingPlatform) Send(ctx context.Context, rctx any, content string) error {
	m.ctxMu.Lock()
	m.sends = append(m.sends, rctx)
	m.ctxMu.Unlock()
	return m.mockKeepPreviewPlatform.Send(ctx, rctx, content)
}

func (m *markingPlatform) Reply(ctx context.Context, rctx any, content string) error {
	return m.Send(ctx, rctx, content)
}

// A plain streamed answer (no tools) is the most common turn: its preview and
// final text must go out marked, or backfill hands the answer back next turn.
func TestAgentOutputIsMarkedOnStreamedTurn(t *testing.T) {
	p := &markingPlatform{}
	p.n = "discord"
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	defer e.Stop()
	sessionKey := "discord:c1"
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := newControllableSession("s1")
	state := &interactiveState{agentSession: agentSession, platform: p, replyCtx: "ctx-1"}
	e.interactiveStates[sessionKey] = state

	agentSession.events <- Event{Type: EventText, Content: "the answer"}
	agentSession.events <- Event{Type: EventResult, Content: "the answer", Done: true}
	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m1", time.Now(), nil, nil, nil)

	p.ctxMu.Lock()
	defer p.ctxMu.Unlock()
	if len(p.previews)+len(p.sends) == 0 {
		t.Fatal("nothing was sent")
	}
	for _, c := range append(append([]any{}, p.previews...), p.sends...) {
		if _, ok := c.(agentMarked); !ok {
			t.Fatalf("agent output sent with unmarked ctx %#v", c)
		}
	}
}
