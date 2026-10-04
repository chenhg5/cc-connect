package core

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCUJ_C7_LateToolOutputDoesNotSplitNextSteeredReply(t *testing.T) {
	p := &observedTextStreamPlatform{observedToolOutputPlatform: observedToolOutputPlatform{toolOutputRichPlatform: toolOutputRichPlatform{stubRichCardSilentPlatform: stubRichCardSilentPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}}}}
	a := &sharedTestAgent{}
	e := NewEngine("shared", a, []Platform{p}, t.TempDir()+"/sessions.json", LangEnglish)
	defer e.Stop()
	e.display.CardMode, e.display.ToolMessages = "rich", true
	send := func(text string) {
		e.ReceiveMessage(p, &Message{SessionKey: "test:user", Platform: "test", UserID: "user", Content: text, ReplyCtx: "reply"})
	}
	await := func(text string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if strings.Contains(strings.Join(p.getSent(), "\n"), text) {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("missing visible %q: %v", text, p.getSent())
	}
	send("/attach first")
	await("Attached to session first")
	as := a.connection("first")
	old := as.RuntimeState().TurnID
	as.emit(Event{Type: EventTurnStarted, TurnID: old})
	as.emit(Event{Type: EventToolUse, TurnID: old, ItemID: "a", ToolName: "Bash", ToolInput: "old Python 1"})
	send("/steer hi")
	as.emit(Event{Type: EventResult, TurnID: old, Content: "FIRST TURN FINAL", Done: true})
	await("FIRST TURN FINAL")
	// Commands outlive the model turn and continue alongside a new CLI turn.
	as.mu.Lock()
	as.turn = "next-turn"
	as.mu.Unlock()
	as.emit(Event{Type: EventToolResult, TurnID: old, ItemID: "a", ToolName: "Bash", ToolResult: "OLD PYTHON 1 RESULT", ToolStatus: "completed"})
	as.emit(Event{Type: EventToolUse, TurnID: old, ItemID: "b", ToolName: "Bash", ToolInput: "old Python 2"})
	as.emit(Event{Type: EventTurnStarted, TurnID: "next-turn"})
	as.emit(Event{Type: EventText, TurnID: "next-turn", ItemID: "body", Content: "明", Metadata: map[string]any{"phase": "commentary", "text_delta": true}})
	as.emit(Event{Type: EventText, TurnID: "next-turn", ItemID: "body", Content: "白，我会继续运行当前打印任务，并收集全部输出", Metadata: map[string]any{"phase": "commentary", "text_delta": true}})
	await("LIVE-BODY 明白，我会继续运行当前打印任务")
	as.emit(Event{Type: EventToolOutput, TurnID: old, ItemID: "b", ToolName: "Bash", Content: "TAIL-LIVE"})
	as.emit(Event{Type: EventToolOutput, TurnID: old, ItemID: "b", ToolName: "Bash", Content: "-MORE"})
	await(`"Result":"TAIL-LIVE-MORE"`)
	send("/steer hi again")
	as.emit(Event{Type: EventToolUse, TurnID: "next-turn", ItemID: "c", ToolName: "Bash", ToolInput: "current Python"})
	as.emit(Event{Type: EventToolResult, TurnID: "next-turn", ItemID: "c", ToolName: "Bash", ToolResult: "CURRENT PYTHON RESULT", ToolStatus: "completed"})
	as.emit(Event{Type: EventResult, TurnID: "next-turn", Content: "SECOND TURN FINAL", Done: true})
	await("SECOND TURN FINAL")
	// The old tool completes even after the next answer. It must update only
	// the old tool card; assistant history must contain neither tool result.
	as.emit(Event{Type: EventToolResult, TurnID: old, ItemID: "b", ToolName: "Bash", ToolResult: "OLD PYTHON 2 RESULT", ToolStatus: "completed"})
	await("OLD PYTHON 2 RESULT")
	await("CURRENT PYTHON RESULT")
	for _, msg := range p.getSent() {
		if msg == "明" || strings.HasPrefix(msg, "白，我会") {
			t.Fatalf("delta leaked as standalone text: %q", msg)
		}
		if strings.Contains(msg, "OLD PYTHON") || strings.Contains(msg, "CURRENT PYTHON RESULT") {
			if !strings.HasPrefix(msg, "TOOL-CARD ") {
				t.Fatalf("tool output escaped panel: %q", msg)
			}
		}
	}
	send("/history 10")
	await("History (last")
	send("/detach")
	await(e.i18n.T(MsgSharedDetached))
}

func TestSharedLateToolNeverInterruptsAnotherTurn(t *testing.T) {
	env := newSharedTestEnv(t)
	env.e.eventIdleTimeout = 100 * time.Millisecond
	env.e.maxTurnTime = 150 * time.Millisecond
	env.send("/attach first")
	env.await("Attached to session first")
	as := env.a.connection("first")
	as.emit(Event{Type: EventResult, TurnID: "active-first", Content: "OLD TURN FINISHED", Done: true})
	env.await("OLD TURN FINISHED")
	as.mu.Lock()
	as.turn = "next-turn"
	as.mu.Unlock()
	as.emit(Event{Type: EventToolResult, TurnID: "active-first", ItemID: "late", ToolName: "Bash", ToolResult: "LATE TOOL FINISHED", ToolStatus: "completed"})
	env.await("LATE TOOL FINISHED")
	time.Sleep(250 * time.Millisecond)
	env.send("/steer after late tool")
	env.await("STEER observed: after late tool")
	if strings.Contains(env.visible(), "timed out") || strings.Contains(env.visible(), "maximum time") {
		t.Fatalf("late tool restarted task timers: %s", env.visible())
	}
	env.send("/detach")
	env.await(env.e.i18n.T(MsgSharedDetached))
}

func TestSharedForegroundTerminalRejectsLateToolBeforeCancellation(t *testing.T) {
	f := &sharedForeground{ctx: context.Background(), events: make(chan Event, 3), bound: true, turnID: "old"}
	if !f.route(Event{Type: EventResult, TurnID: "old", Done: true}) {
		t.Fatal("terminal event refused")
	}
	if f.route(Event{Type: EventToolResult, TurnID: "old", ItemID: "late"}) {
		t.Fatal("tool result queued behind terminal event and lost")
	}
}

type delayedBindingAgent struct {
	*sharedCompatAgent
	release chan struct{}
}

func (a *delayedBindingAgent) StartSession(ctx context.Context, id string) (AgentSession, error) {
	as, err := a.sharedCompatAgent.StartSession(ctx, id)
	if err != nil {
		return nil, err
	}
	return &delayedBindingSession{sharedCompatSession: as.(*sharedCompatSession), release: a.release}, nil
}

type delayedBindingSession struct {
	*sharedCompatSession
	release chan struct{}
}

func (s *delayedBindingSession) SendTurn(prompt, id string, images []ImageAttachment, files []FileAttachment) (string, error) {
	turn, err := s.sharedCompatSession.SendTurn(prompt, id, images, files)
	<-s.release
	return turn, err
}

func TestCUJ_C7_BindingBufferKeepsForeignDeltasInCard(t *testing.T) {
	p := &observedTextStreamPlatform{observedToolOutputPlatform: observedToolOutputPlatform{toolOutputRichPlatform: toolOutputRichPlatform{stubRichCardSilentPlatform: stubRichCardSilentPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}}}}
	base := &sharedCompatAgent{calls: make(chan sharedCompatCall, 8)}
	a := &delayedBindingAgent{sharedCompatAgent: base, release: make(chan struct{})}
	e := NewEngine("shared", a, []Platform{p}, t.TempDir()+"/sessions.json", LangEnglish)
	defer e.Stop()
	e.display.CardMode = "rich"
	send := func(text string) {
		e.ReceiveMessage(p, &Message{SessionKey: "test:user", Platform: "test", UserID: "user", Content: text, ReplyCtx: "reply"})
	}
	await := func(text string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if strings.Contains(strings.Join(p.getSent(), "\n"), text) {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("missing visible %q: %v", text, p.getSent())
	}
	send("local task")
	call := awaitSharedCompatCall(t, base)
	call.session.emit(Event{Type: EventTurnStarted, TurnID: "foreign"})
	call.session.emit(Event{Type: EventText, TurnID: "foreign", Content: "FOREIGN PREFIX ", Metadata: map[string]any{"phase": "commentary", "text_delta": true}})
	call.session.emit(Event{Type: EventText, TurnID: "foreign", Content: "FOREIGN LONG TAIL INCREMENT", Metadata: map[string]any{"phase": "commentary", "text_delta": true}})
	// Reader-order barrier before releasing the SendTurn response.
	call.session.emit(Event{Type: EventRuntimeStatus, Content: "connected"})
	await(e.i18n.T(MsgSharedReconnected))
	close(a.release)
	await("LIVE-BODY FOREIGN PREFIX FOREIGN LONG TAIL INCREMENT")
	call.session.emit(Event{Type: EventResult, TurnID: "foreign", Content: "FOREIGN FINAL", Done: true})
	call.finish("LOCAL FINAL")
	await("LOCAL FINAL")
	send("next local task")
	next := awaitSharedCompatCall(t, base)
	next.finish("NEXT LOCAL FINAL")
	await("NEXT LOCAL FINAL")
	send("/history 10")
	await("History (last")
	for _, msg := range p.getSent() {
		if msg == "FOREIGN PREFIX " || msg == "FOREIGN LONG TAIL INCREMENT" {
			t.Fatalf("unbound event escaped card: %q", msg)
		}
	}
}
