package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type toolOutputRichPlatform struct{ stubRichCardSilentPlatform }

// Expose card starts/edits as platform-visible messages for CUJ assertions.
type observedToolOutputPlatform struct{ toolOutputRichPlatform }

func (p *observedToolOutputPlatform) SendPreviewStart(ctx context.Context, reply any, content string) (any, error) {
	handle, err := p.stubRichCardSilentPlatform.SendPreviewStart(ctx, reply, content)
	if err == nil {
		_ = p.Send(ctx, reply, content)
	}
	return handle, err
}

func (p *observedToolOutputPlatform) UpdateMessage(ctx context.Context, handle any, content string) error {
	if err := p.stubRichCardSilentPlatform.UpdateMessage(ctx, handle, content); err != nil {
		return err
	}
	return p.Send(ctx, handle, content)
}

func (p *toolOutputRichPlatform) BuildRichCard(status CardStatus, _ string, steps []ToolStep, markdown string, _ bool, _ string) string {
	data, _ := json.Marshal(steps)
	return "TOOL-CARD " + string(status) + " " + string(data) + " " + markdown
}

func TestSharedToolOutputDoesNotLeakAsAssistantText(t *testing.T) {
	for _, mode := range []string{"rich", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			p := &toolOutputRichPlatform{stubRichCardSilentPlatform: stubRichCardSilentPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}}
			e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
			defer e.Stop()
			e.display.CardMode, e.display.ToolMessages = mode, true
			session := e.sessions.GetOrCreateActive("test:user")
			as := newControllableSession("thread")
			state := &interactiveState{agentSession: as, platform: p, replyCtx: "reply"}
			routed := make(chan Event, 4)
			routed <- Event{Type: EventToolOutput, ToolName: "Bash", Content: "RAW-STDOUT-MARKER"}
			routed <- Event{Type: EventToolResult, ToolName: "Bash", ToolResult: "RAW-STDOUT-MARKER", ToolStatus: "completed"}
			routed <- Event{Type: EventResult, Content: "FINAL REPLY", Done: true}
			e.processInteractiveEvents(state, session, e.sessions, "test:user", "msg", time.Now(), nil, nil, "reply", 0, routed)
			for _, text := range p.getSent() {
				if text == "RAW-STDOUT-MARKER" {
					t.Fatal("transport stdout leaked as assistant reply")
				}
			}
			if mode == "rich" {
				starts, _, updates, _ := p.snapshot()
				if !strings.Contains(strings.Join(append(starts, updates...), "\n"), "RAW-STDOUT-MARKER") {
					t.Fatal("completed tool output lost from rich tool card")
				}
			}
		})
	}
}

func TestSharedUnscopedToolOutputDoesNotLeakAsAssistantText(t *testing.T) {
	env := newSharedTestEnv(t)
	env.send("/attach first")
	env.await("Attached to session first")
	as := env.a.connection("first")
	as.emit(Event{Type: EventToolOutput, ItemID: "command", Content: "UNSCOPED-STDOUT"})
	as.emit(Event{Type: EventResult, Content: "FINAL AFTER OUTPUT", Done: true})
	env.await("FINAL AFTER OUTPUT")
	for _, text := range env.p.getSent() {
		if text == "UNSCOPED-STDOUT" {
			t.Fatal("shared reader flushed stdout as assistant reply")
		}
	}
}

func TestLiveToolOutputUsesItemIDsAndFinalResult(t *testing.T) {
	steps := []ToolStep{{Kind: ToolStepKindTool, Name: "Bash", ItemID: "a"}, {Kind: ToolStepKindTool, Name: "Bash", ItemID: "b"}}
	steps = mergeLiveToolOutput(steps, Event{ItemID: "a", ToolName: "Bash", Content: "甲乙"}, 3)
	steps = mergeLiveToolOutput(steps, Event{ItemID: "b", ToolName: "Bash", Content: "other"}, 20)
	steps = mergeLiveToolOutput(steps, Event{ItemID: "a", ToolName: "Bash", Content: "丙丁"}, 3)
	if steps[0].Result != "乙丙丁" || steps[1].Result != "other" || steps[0].Done || steps[1].Done {
		t.Fatalf("mixed/truncated live output incorrectly: %+v", steps)
	}
	steps = mergeRichToolResult(steps, Event{ItemID: "a", ToolName: "Bash", ToolStatus: "completed"}, "AUTHORITATIVE RESULT", 500)
	steps = mergeLiveToolOutput(steps, Event{ItemID: "a", ToolName: "Bash", Content: "late delta"}, 500)
	if len(steps) != 2 || steps[0].Result != "AUTHORITATIVE RESULT" || !steps[0].Done || steps[1].Done {
		t.Fatalf("final result did not replace only its invocation: %+v", steps)
	}
}

func TestSharedRichToolOutputIsVisibleBeforeCompletionAndKeepsAsyncText(t *testing.T) {
	p := &toolOutputRichPlatform{stubRichCardSilentPlatform: stubRichCardSilentPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	defer e.Stop()
	e.display.CardMode, e.display.ToolMessages = "rich", true
	session := e.sessions.GetOrCreateActive("test:user")
	as := newControllableSession("thread")
	state := &interactiveState{agentSession: as, platform: p, replyCtx: "reply"}
	routed := make(chan Event, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.processInteractiveEvents(state, session, e.sessions, "test:user", "msg", time.Now(), nil, nil, "reply", 0, routed)
	}()
	routed <- Event{Type: EventToolUse, ItemID: "command", ToolName: "Bash", ToolInput: "long command"}
	routed <- Event{Type: EventText, Content: "ASYNC QUESTION: choose a direction", Metadata: map[string]any{"phase": "commentary", "delivery": "async"}}
	routed <- Event{Type: EventToolOutput, ItemID: "command", ToolName: "Bash", Content: "LIVE-FIRST"}
	routed <- Event{Type: EventToolOutput, ItemID: "command", ToolName: "Bash", Content: "-SECOND"}
	deadline := time.Now().Add(3 * time.Second)
	visible := false
	for time.Now().Before(deadline) {
		starts, _, updates, _ := p.snapshot()
		all := strings.Join(append(starts, updates...), "\n")
		if strings.Contains(all, `"Result":"LIVE-FIRST-SECOND"`) && strings.Contains(all, "ASYNC QUESTION") {
			visible = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Completion is deliberately withheld until the live-output assertions.
	routed <- Event{Type: EventToolResult, ItemID: "command", ToolName: "Bash", ToolResult: "FINAL-TOOL-OUTPUT", ToolStatus: "completed"}
	routed <- Event{Type: EventResult, Content: "FINAL REPLY", Done: true}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("presentation did not finish")
	}
	if !visible {
		t.Fatal("live output or async question hidden until tool completion")
	}
	starts, _, updates, _ := p.snapshot()
	if !strings.Contains(strings.Join(append(starts, updates...), "\n"), "FINAL-TOOL-OUTPUT") {
		t.Fatal("authoritative completed output missing")
	}
}

// Keep the embedded preview implementation visible to interface assertions.
var _ PreviewStarter = (*toolOutputRichPlatform)(nil)
