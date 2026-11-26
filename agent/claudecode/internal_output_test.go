package claudecode

import (
	"context"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestClaudeInternalReminderNotForwarded(t *testing.T) {
	const hidden = "PRIVATE_INTERNAL_REMINDER"
	content := "Answer before.\n<system-reminder>" + hidden + "</system-reminder>\nAnswer after."
	cs := &claudeSession{events: make(chan core.Event, 4), ctx: context.Background()}
	cs.handleAssistant(map[string]any{"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": content}}}})
	cs.handleResult(map[string]any{"result": content})
	for _, want := range []core.EventType{core.EventText, core.EventResult} {
		event := <-cs.events
		if event.Type != want {
			t.Fatalf("event type = %q, want %q", event.Type, want)
		}
		if strings.Contains(event.Content, hidden) || strings.Contains(event.Content, "system-reminder") {
			t.Fatalf("internal reminder leaked in %q: %q", event.Type, event.Content)
		}
		if !strings.Contains(event.Content, "Answer before.") || !strings.Contains(event.Content, "Answer after.") {
			t.Fatalf("visible answer lost in %q: %q", event.Type, event.Content)
		}
	}
}

func TestClaudeResumeInstructionNotForwarded(t *testing.T) {
	content := "Continue from where you left off\nThe final answer."
	cs := &claudeSession{events: make(chan core.Event, 4), ctx: context.Background()}
	cs.handleAssistant(map[string]any{"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": content}}}})
	cs.handleResult(map[string]any{"result": content})
	for range 2 {
		event := <-cs.events
		if strings.Contains(event.Content, "Continue from where you left off") || !strings.Contains(event.Content, "The final answer.") {
			t.Fatalf("unexpected forwarded output: %q", event.Content)
		}
	}
}
