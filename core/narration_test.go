package core

import (
	"strings"
	"testing"
	"time"
)

func newNarrationEngine(t *testing.T, p Platform, narration string) *Engine {
	t.Helper()
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	e.SetDisplayConfig(DisplayCfg{Mode: "full", ThinkingMessages: true, ToolMessages: true, Narration: narration})
	return e
}

func runNarrationTurn(t *testing.T, e *Engine, p Platform, key string, events ...Event) *Session {
	t.Helper()
	session := e.sessions.GetOrCreateActive(key)
	s := newControllableSession("narration")
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

func newProgressCardPlatform() *stubCompactProgressPlatform {
	return &stubCompactProgressPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}, style: "card", supportPayload: true}
}

// lastProgressItems is the progress card as it was last drawn.
func lastProgressItems(t *testing.T, p *stubCompactProgressPlatform) []ProgressCardEntry {
	t.Helper()
	drawn := append(p.getPreviewStarts(), p.getPreviewEdits()...)
	for _, content := range drawn {
		if !strings.HasPrefix(content, ProgressCardPayloadPrefix) {
			t.Fatalf("drew something other than the progress card (a text preview?): %q", content)
		}
	}
	if len(drawn) == 0 {
		t.Fatal("no progress card was drawn")
	}
	payload, ok := ParseProgressCardPayload(drawn[len(drawn)-1])
	if !ok {
		t.Fatalf("unparsable progress card: %q", drawn[len(drawn)-1])
	}
	return payload.Items
}

func infoTexts(items []ProgressCardEntry) []string {
	var out []string
	for _, item := range items {
		if item.Kind == ProgressEntryInfo {
			out = append(out, item.Text)
		}
	}
	return out
}

// Narration "progress": text before a tool call or thinking block becomes
// an update in the progress card; the answer is the turn's only message.
func TestProcessInteractiveEvents_NarrationInProgressCard(t *testing.T) {
	p := newProgressCardPlatform()
	e := newNarrationEngine(t, p, narrationProgress)
	session := runNarrationTurn(t, e, p, "test:narration",
		Event{Type: EventText, Content: "Let me check the board."},
		Event{Type: EventToolUse, ToolName: "Bash", ToolInput: "orb task list"},
		Event{Type: EventToolResult, ToolName: "Bash", ToolResult: "ok"},
		Event{Type: EventThinking, Content: "planning"},
		Event{Type: EventText, Content: "Two tasks are open."},
		Event{Type: EventToolUse, ToolName: "Bash", ToolInput: "orb task show 1"},
		Event{Type: EventToolResult, ToolName: "Bash", ToolResult: "ok"},
		Event{Type: EventText, Content: "Answer: "},
		Event{Type: EventText, Content: "both are mine."},
		Event{Type: EventResult, Content: "Answer: both are mine.", Done: true},
	)
	if sent := p.getSent(); len(sent) != 1 || sent[0] != "Answer: both are mine." {
		t.Fatalf("sent = %q, want only the answer", sent)
	}
	got := strings.Join(infoTexts(lastProgressItems(t, p)), "|")
	if got != "Let me check the board.|Two tasks are open." {
		t.Fatalf("progress card updates = %q", got)
	}
	history := session.GetHistory(0)
	if len(history) == 0 || history[len(history)-1].Content != "Answer: both are mine." {
		t.Fatalf("history = %+v", history)
	}
}

// A turn that ends on a tool call answered before it: that narration is
// sent as the answer, after having been an update in the card.
func TestProcessInteractiveEvents_NarrationAnswerBeforeLastTool(t *testing.T) {
	p := newProgressCardPlatform()
	e := newNarrationEngine(t, p, narrationProgress)
	runNarrationTurn(t, e, p, "test:narration-tail",
		Event{Type: EventText, Content: "Looking."},
		Event{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"},
		Event{Type: EventToolResult, ToolName: "Bash", ToolResult: "ok"},
		Event{Type: EventText, Content: "The answer is 42."},
		Event{Type: EventToolUse, ToolName: "Bash", ToolInput: "orb task finish"},
		Event{Type: EventToolResult, ToolName: "Bash", ToolResult: "ok"},
		Event{Type: EventResult, Done: true},
	)
	if sent := p.getSent(); len(sent) != 1 || sent[0] != "The answer is 42." {
		t.Fatalf("sent = %q, want the last narration as the answer", sent)
	}
}

// NO_REPLY narration is neither an update nor the answer.
func TestProcessInteractiveEvents_NarrationSilentSegment(t *testing.T) {
	p := newProgressCardPlatform()
	e := newNarrationEngine(t, p, narrationProgress)
	runNarrationTurn(t, e, p, "test:narration-silent",
		Event{Type: EventText, Content: "NO_REPLY"},
		Event{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"},
		Event{Type: EventToolResult, ToolName: "Bash", ToolResult: "ok"},
		Event{Type: EventText, Content: "Done."},
		Event{Type: EventResult, Content: "Done.", Done: true},
	)
	if sent := p.getSent(); len(sent) != 1 || sent[0] != "Done." {
		t.Fatalf("sent = %q", sent)
	}
	if updates := infoTexts(lastProgressItems(t, p)); len(updates) != 0 {
		t.Fatalf("silent narration reached the card: %q", updates)
	}
}

// Without progress cards there is nowhere else to put narration: it is sent
// as messages, as with the default.
func TestProcessInteractiveEvents_NarrationWithoutProgressCard(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := newNarrationEngine(t, p, narrationProgress)
	runNarrationTurn(t, e, p, "test:narration-plain",
		Event{Type: EventText, Content: "Let me check."},
		Event{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"},
		Event{Type: EventText, Content: "Answer."},
		Event{Type: EventResult, Content: "Answer.", Done: true},
	)
	sent := strings.Join(p.getSent(), "\n")
	for _, want := range []string{"Let me check.", "Answer."} {
		if strings.Count(sent, want) != 1 {
			t.Fatalf("%q sent %d times; sent=%q", want, strings.Count(sent, want), sent)
		}
	}
}

// The default keeps narration as messages of their own.
func TestProcessInteractiveEvents_NarrationMessageIsTheDefault(t *testing.T) {
	p := newProgressCardPlatform()
	e := newNarrationEngine(t, p, "")
	runNarrationTurn(t, e, p, "test:narration-default",
		Event{Type: EventText, Content: "Let me check."},
		Event{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"},
		Event{Type: EventToolResult, ToolName: "Bash", ToolResult: "ok"},
		Event{Type: EventText, Content: "Answer."},
		Event{Type: EventResult, Content: "Answer.", Done: true},
	)
	visible := strings.Join(append(p.getSent(), p.getPreviewStarts()...), "\n")
	for _, want := range []string{"Let me check.", "Answer."} {
		if !strings.Contains(visible, want) {
			t.Fatalf("%q not sent as a message; sent=%q", want, visible)
		}
	}
}

// Narration that could not go into a card was sent as a message already: a
// turn ending on a tool call does not send it again.
func TestProcessInteractiveEvents_NarrationSentAsMessageIsNotRepeated(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := newNarrationEngine(t, p, narrationProgress)
	runNarrationTurn(t, e, p, "test:narration-plain-tail",
		Event{Type: EventText, Content: "The answer is 42."},
		Event{Type: EventToolUse, ToolName: "Bash", ToolInput: "orb task finish"},
		Event{Type: EventResult, Done: true},
	)
	if sent := strings.Join(p.getSent(), "\n"); strings.Count(sent, "The answer is 42.") != 1 {
		t.Fatalf("narration sent %d times: %q", strings.Count(sent, "The answer is 42."), sent)
	}
}

// A Stop-hook-rejected draft's narration is not the rewritten turn's answer.
func TestProcessInteractiveEvents_NarrationForgottenOnHookRejection(t *testing.T) {
	p := newProgressCardPlatform()
	e := newNarrationEngine(t, p, narrationProgress)
	runNarrationTurn(t, e, p, "test:narration-hook",
		Event{Type: EventText, Content: "Draft answer."},
		Event{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"},
		Event{Type: EventHookRejected},
		Event{Type: EventText, Content: "NO_REPLY"},
		Event{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"},
		Event{Type: EventResult, Done: true},
	)
	if sent := p.getSent(); strings.Contains(strings.Join(sent, "\n"), "Draft answer.") {
		t.Fatalf("the rejected draft was sent as the answer: %q", sent)
	}
}

// queuedTurnSession answers the queued message with the next turn's events,
// once the engine has sent it (stale events are drained before that).
type queuedTurnSession struct {
	*controllableAgentSession
	next []Event
}

func (s *queuedTurnSession) Send(string, string, []ImageAttachment, []FileAttachment) error {
	go func() {
		for _, ev := range s.next {
			s.events <- ev
		}
	}()
	return nil
}

// A queued turn starts without the previous turn's narration.
func TestProcessInteractiveEvents_NarrationForgottenBetweenQueuedTurns(t *testing.T) {
	p := newProgressCardPlatform()
	e := newNarrationEngine(t, p, narrationProgress)
	key := "test:narration-queued"
	session := e.sessions.GetOrCreateActive(key)
	s := &queuedTurnSession{controllableAgentSession: newControllableSession("narration-queued"), next: []Event{
		{Type: EventText, Content: "NO_REPLY"},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"},
		{Type: EventResult, Done: true},
	}}
	for _, ev := range []Event{
		{Type: EventText, Content: "First answer."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"},
		{Type: EventResult, Done: true},
	} {
		s.events <- ev
	}
	state := &interactiveState{agentSession: s, platform: p, replyCtx: "ctx",
		pendingMessages: []queuedMessage{{platform: p, replyCtx: "ctx", content: "next", messageID: "m2"}}}
	done := make(chan struct{})
	go func() {
		e.processInteractiveEvents(state, session, e.sessions, key, "m1", time.Now(), nil, nil, "ctx", 0)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("turns did not complete")
	}
	if sent := strings.Join(p.getSent(), "\n"); strings.Count(sent, "First answer.") != 1 {
		t.Fatalf("first answer sent %d times: %q", strings.Count(sent, "First answer."), sent)
	}
}
