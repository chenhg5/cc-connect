package core

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

type trackedRichPermissionPlatform struct {
	stubRichCardSilentPlatform
	trackerMu    sync.Mutex
	updateHandle []any
	streamHandle []any
}

func (p *trackedRichPermissionPlatform) UpdateMessage(ctx context.Context, handle any, content string) error {
	p.trackerMu.Lock()
	p.updateHandle = append(p.updateHandle, handle)
	p.trackerMu.Unlock()
	return p.stubRichCardSilentPlatform.UpdateMessage(ctx, handle, content)
}

func (p *trackedRichPermissionPlatform) StreamRichCardText(ctx context.Context, handle any, content string) error {
	p.trackerMu.Lock()
	p.streamHandle = append(p.streamHandle, handle)
	p.trackerMu.Unlock()
	return p.stubRichCardSilentPlatform.StreamRichCardText(ctx, handle, content)
}

func TestRichCardPermissionContinuationOpensNewCard(t *testing.T) {
	p := &trackedRichPermissionPlatform{
		stubRichCardSilentPlatform: stubRichCardSilentPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}},
	}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetDisplayConfig(DisplayCfg{Mode: "full", CardMode: "rich", ToolMessages: true})
	key := "feishu:rich-permission-continuation"
	session := e.sessions.GetOrCreateActive(key)
	agentSession := newControllableSession("s-rich-permission-continuation")
	state := &interactiveState{agentSession: agentSession, platform: p, replyCtx: "ctx"}
	e.interactiveStates[key] = state

	agentSession.events <- Event{Type: EventText, Content: "before permission"}
	agentSession.events <- Event{Type: EventPermissionRequest, RequestID: "r1", ToolName: "Bash", ToolInput: "echo ok"}
	agentSession.events <- Event{Type: EventText, Content: "after permission"}
	agentSession.events <- Event{Type: EventResult, Content: "after permission", Done: true}

	done := make(chan struct{})
	go func() {
		e.processInteractiveEvents(state, session, e.sessions, key, "m1", time.Now(), nil, nil, state.replyCtx, 0)
		close(done)
	}()

	deadline := time.After(5 * time.Second)
	for {
		state.mu.Lock()
		pending := state.pending
		state.mu.Unlock()
		if pending != nil {
			pending.resolve()
			break
		}
		select {
		case <-deadline:
			t.Fatal("permission request was not reached")
		case <-time.After(5 * time.Millisecond):
		}
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not finish after permission resolution")
	}

	starts, _, updates, _ := p.snapshot()
	if len(starts) != 2 {
		t.Fatalf("rich card starts = %v; want separate cards before and after permission", starts)
	}
	if strings.Contains(starts[1], "before permission") || !strings.Contains(starts[1], "after permission") {
		t.Fatalf("continuation card contains wrong text: %q", starts[1])
	}
	p.trackerMu.Lock()
	handles := append([]any(nil), p.updateHandle...)
	p.trackerMu.Unlock()
	if len(handles) < 2 || handles[0] != "handle-1" || handles[len(handles)-1] != "handle-2" {
		t.Fatalf("card update handles = %v; want old card frozen and new card finalized", handles)
	}
	if !strings.Contains(updates[len(updates)-1], "after permission") || strings.Contains(updates[len(updates)-1], "before permission") {
		t.Fatalf("final continuation card = %q", updates[len(updates)-1])
	}
}

func TestRichCardPermissionAtTurnEndDoesNotSendEmptyCard(t *testing.T) {
	p := &trackedRichPermissionPlatform{
		stubRichCardSilentPlatform: stubRichCardSilentPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}},
	}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetDisplayConfig(DisplayCfg{Mode: "full", CardMode: "rich", ToolMessages: true})
	key := "feishu:rich-permission-at-end"
	session := e.sessions.GetOrCreateActive(key)
	agentSession := newControllableSession("s-rich-permission-at-end")
	state := &interactiveState{agentSession: agentSession, platform: p, replyCtx: "ctx"}
	e.interactiveStates[key] = state
	agentSession.events <- Event{Type: EventText, Content: "before permission"}
	agentSession.events <- Event{Type: EventPermissionRequest, RequestID: "r1", ToolName: "Bash", ToolInput: "echo ok"}
	agentSession.events <- Event{Type: EventResult, Content: "before permission", Done: true}

	done := make(chan struct{})
	go func() {
		e.processInteractiveEvents(state, session, e.sessions, key, "m1", time.Now(), nil, nil, state.replyCtx, 0)
		close(done)
	}()
	deadline := time.After(5 * time.Second)
	for {
		state.mu.Lock()
		pending := state.pending
		state.mu.Unlock()
		if pending != nil {
			pending.resolve()
			break
		}
		select {
		case <-deadline:
			t.Fatal("permission request was not reached")
		case <-time.After(5 * time.Millisecond):
		}
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not finish")
	}
	starts, _, updates, _ := p.snapshot()
	if len(starts) != 1 || len(updates) != 1 || !strings.Contains(updates[0], "before permission") {
		t.Fatalf("card operations: starts=%v updates=%v; want one finalized pre-permission card", starts, updates)
	}
	if sent := p.getSent(); len(sent) != 1 {
		t.Fatalf("sent messages = %v; want only permission prompt and no empty completion card", sent)
	}
}
