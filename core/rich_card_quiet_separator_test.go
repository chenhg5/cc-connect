package core

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// stubQuietSeparatorPlatform records the raw markdown handed to BuildRichCard
// so a test can assert on the card text itself instead of on a quoted
// rendering of it.
type stubQuietSeparatorPlatform struct {
	stubPlatformEngine
	mu      sync.Mutex
	bodies  []string
	updates []string
}

func (p *stubQuietSeparatorPlatform) BuildRichCard(_ CardStatus, _ string, _ []ToolStep, markdown string, _ bool, _ string) string {
	p.mu.Lock()
	p.bodies = append(p.bodies, markdown)
	p.mu.Unlock()
	return "card"
}

func (p *stubQuietSeparatorPlatform) SendPreviewStart(_ context.Context, _ any, _ string) (any, error) {
	return "handle", nil
}

func (p *stubQuietSeparatorPlatform) UpdateMessage(_ context.Context, _ any, content string) error {
	p.mu.Lock()
	p.updates = append(p.updates, content)
	p.mu.Unlock()
	return nil
}

func (p *stubQuietSeparatorPlatform) recordedBodies() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.bodies))
	copy(out, p.bodies)
	return out
}

func (p *stubQuietSeparatorPlatform) recordedUpdates() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.updates))
	copy(out, p.updates)
	return out
}

// runQuietSeparatorScenario drives processInteractiveEvents with the given
// display config and events, and returns every card body that was rendered
// plus every message the platform received.
func runQuietSeparatorScenario(t *testing.T, name string, cfg DisplayCfg, events []Event) (bodies []string, updates []string, sent []string) {
	t.Helper()
	p := &stubQuietSeparatorPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetDisplayConfig(cfg)
	sessionKey := "feishu:user-quiet-sep-" + name
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := newControllableSession("s-quiet-sep-" + name)
	state := &interactiveState{
		agentSession: agentSession,
		platform:     p,
		replyCtx:     "ctx-quiet-sep-" + name,
	}
	e.interactiveStates[sessionKey] = state
	for _, ev := range events {
		agentSession.events <- ev
	}
	agentSession.events <- Event{Type: EventResult, Content: "GAMMA", Done: true}
	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m-quiet-sep-"+name, time.Now(), nil, nil, state.replyCtx, 0)
	return p.recordedBodies(), p.recordedUpdates(), p.getSent()
}

func quietSeparatorConfig(cardMode string) DisplayCfg {
	return DisplayCfg{
		Mode:             "quiet",
		CardMode:         cardMode,
		ThinkingMessages: false,
		ThinkingMaxLen:   300,
		ToolMaxLen:       500,
		ToolMessages:     false,
	}
}

// hasSeparatedParagraph reports whether any rendered body separates the two
// assistant messages with a blank line.
func hasSeparatedParagraph(bodies []string) bool {
	for _, b := range bodies {
		if strings.Contains(b, "ALPHA\n\nGAMMA") {
			return true
		}
	}
	return false
}

// A hidden tool call splits the turn into two assistant messages. Quiet mode
// keeps them in one card, so they need a blank line between them — otherwise
// the card shows "ALPHAGAMMA" as one run of text.
func TestQuietRichCard_SeparatesAssistantMessagesAroundHiddenToolCall(t *testing.T) {
	bodies, _, _ := runQuietSeparatorScenario(t, "tool", quietSeparatorConfig("rich"), []Event{
		{Type: EventText, Content: "ALPHA"},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "echo BETA"},
		{Type: EventText, Content: "GAMMA"},
	})
	if !hasSeparatedParagraph(bodies) {
		t.Fatalf("rich+quiet card should separate the two assistant messages with a blank line, got bodies %q", bodies)
	}
}

// Same boundary, but hidden behind a thinking block instead of a tool call.
func TestQuietRichCard_SeparatesAssistantMessagesAroundHiddenThinking(t *testing.T) {
	bodies, _, _ := runQuietSeparatorScenario(t, "thinking", quietSeparatorConfig("rich"), []Event{
		{Type: EventText, Content: "ALPHA"},
		{Type: EventThinking, Content: "pondering"},
		{Type: EventText, Content: "GAMMA"},
	})
	if !hasSeparatedParagraph(bodies) {
		t.Fatalf("rich+quiet card should separate the two assistant messages with a blank line, got bodies %q", bodies)
	}
}

// The legacy card path already separated the messages; make sure the rich
// fix did not change that path. Legacy renders through the platform's card
// builder rather than BuildRichCard, so assert on the preview-card update.
func TestQuietLegacyCard_StillSeparatesAssistantMessages(t *testing.T) {
	_, updates, _ := runQuietSeparatorScenario(t, "legacy", quietSeparatorConfig("legacy"), []Event{
		{Type: EventText, Content: "ALPHA"},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "echo BETA"},
		{Type: EventText, Content: "GAMMA"},
	})
	separated := false
	for _, msg := range updates {
		if strings.Contains(msg, "ALPHA\n\nGAMMA") {
			separated = true
		}
	}
	if !separated {
		t.Fatalf("legacy+quiet card should separate the two assistant messages with a blank line, got updates %q", updates)
	}
}

// Outside quiet mode the tool call is surfaced as its own block, so the two
// assistant messages are not blank-line separated.
func TestFullRichCard_DoesNotSeparateAssistantMessages(t *testing.T) {
	cfg := quietSeparatorConfig("rich")
	cfg.Mode = "full"
	cfg.ToolMessages = true
	cfg.ThinkingMessages = true
	bodies, _, _ := runQuietSeparatorScenario(t, "full", cfg, []Event{
		{Type: EventText, Content: "ALPHA"},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "echo BETA"},
		{Type: EventText, Content: "GAMMA"},
	})
	if hasSeparatedParagraph(bodies) {
		t.Fatalf("full-mode card should not blank-line separate the assistant messages, got bodies %q", bodies)
	}
}

// A turn with a single assistant message must not gain a trailing blank line.
func TestQuietRichCard_SingleMessageHasNoSeparator(t *testing.T) {
	bodies, _, _ := runQuietSeparatorScenario(t, "single", quietSeparatorConfig("rich"), []Event{
		{Type: EventText, Content: "ALPHA"},
	})
	for _, b := range bodies {
		if strings.Contains(b, "\n\n") {
			t.Fatalf("single-message card should not contain a blank line, got body %q", b)
		}
	}
}
