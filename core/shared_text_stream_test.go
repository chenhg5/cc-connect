package core

import (
	"context"
	"strings"
	"testing"
	"time"
)

type observedTextStreamPlatform struct{ observedToolOutputPlatform }

func (p *observedTextStreamPlatform) StreamRichCardText(ctx context.Context, handle any, text string) error {
	if err := p.stubRichCardSilentPlatform.StreamRichCardText(ctx, handle, text); err != nil {
		return err
	}
	return p.Send(ctx, handle, "LIVE-BODY "+text)
}

func TestSharedTextSnapshotReplacesOnlyItsItemAndKeepsPunctuation(t *testing.T) {
	p := &toolOutputRichPlatform{stubRichCardSilentPlatform: stubRichCardSilentPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}}
	e := NewEngine("shared", &stubAgent{}, []Platform{p}, "", LangEnglish)
	defer e.Stop()
	e.display.CardMode = "rich"
	session := e.sessions.GetOrCreateActive("test:user")
	state := &interactiveState{agentSession: newControllableSession("thread"), platform: p, replyCtx: "reply"}
	events := make(chan Event, 8)
	events <- Event{Type: EventText, TurnID: "t", ItemID: "preface", Content: "PREFACE ", Metadata: map[string]any{"text_delta": true}}
	events <- Event{Type: EventText, TurnID: "t", ItemID: "answer", Content: "PARTIAL", Metadata: map[string]any{"text_delta": true}}
	events <- Event{Type: EventText, TurnID: "t", ItemID: "answer", Content: "...", Metadata: map[string]any{"text_delta": true}}
	events <- Event{Type: EventText, TurnID: "t", ItemID: "answer", Content: strings.Repeat("X", 40), Metadata: map[string]any{"text_delta": true}}
	events <- Event{Type: EventText, TurnID: "t", ItemID: "answer", Content: "CORRECTED...ANSWER", Metadata: map[string]any{"replace_item_text": true}}
	events <- Event{Type: EventResult, TurnID: "t", Content: "PREFACE CORRECTED...ANSWER", Done: true}
	e.processInteractiveEvents(state, session, e.sessions, "test:user", "msg", time.Now(), nil, nil, "reply", 0, events)
	_, streams, updates, _ := p.snapshot()
	if !strings.Contains(strings.Join(streams, "\n"), "PARTIAL...") {
		t.Fatal("punctuation delta was dropped")
	}
	if len(streams) == 0 || streams[len(streams)-1] != "PREFACE CORRECTED...ANSWER" {
		t.Fatalf("incorrect final stream: %v", streams)
	}
	if len(updates) == 0 || strings.Contains(updates[len(updates)-1], "PARTIAL") || !strings.Contains(updates[len(updates)-1], "PREFACE CORRECTED...ANSWER") {
		t.Fatalf("bad authoritative final card: %v", updates)
	}
}

func waitTextStreamVisible(t *testing.T, p *observedTextStreamPlatform, text string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(strings.Join(p.getSent(), "\n"), text) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("missing live text %q: %v", text, p.getSent())
}

// Platforms without the native text API retain live full-card fallback updates.
type unsupportedTextStreamPlatform struct{ toolOutputRichPlatform }

func (*unsupportedTextStreamPlatform) StreamRichCardText(context.Context, any, string) error {
	return ErrNotSupported
}

func TestSharedUnsupportedTextStreamingFallsBackBeforeTurnCompletion(t *testing.T) {
	p := &unsupportedTextStreamPlatform{toolOutputRichPlatform: toolOutputRichPlatform{stubRichCardSilentPlatform: stubRichCardSilentPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}}}
	e := NewEngine("shared", &stubAgent{}, []Platform{p}, "", LangEnglish)
	defer e.Stop()
	e.display.CardMode = "rich"
	session := e.sessions.GetOrCreateActive("test:user")
	state := &interactiveState{agentSession: newControllableSession("thread"), platform: p, replyCtx: "reply"}
	events := make(chan Event, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.processInteractiveEvents(state, session, e.sessions, "test:user", "msg", time.Now(), nil, nil, "reply", 0, events)
	}()
	events <- Event{Type: EventText, Content: "FIRST LIVE INCREMENT", Metadata: map[string]any{"text_delta": true}}
	events <- Event{Type: EventText, Content: " SECOND LIVE INCREMENT THAT TRIGGERS UPDATE", Metadata: map[string]any{"text_delta": true}}
	visible := false
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, streams, updates, _ := p.snapshot()
		if len(streams) > 0 {
			t.Error("unsupported native streaming unexpectedly succeeded")
		}
		if strings.Contains(strings.Join(updates, "\n"), "FIRST LIVE INCREMENT SECOND LIVE INCREMENT") {
			visible = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	events <- Event{Type: EventResult, Content: "FIRST LIVE INCREMENT SECOND LIVE INCREMENT THAT TRIGGERS UPDATE", Done: true}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("presentation did not complete")
	}
	if !visible {
		t.Fatal("full-card fallback buffered text until turn completion")
	}
}
