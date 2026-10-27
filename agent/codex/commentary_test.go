package codex

import (
	"context"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestCodexCommentary_IsDeliveredBeforeNextToolOrTurnEnd(t *testing.T) {
	for _, backend := range []string{"app_server", "exec"} {
		t.Run(backend, func(t *testing.T) {
			events := make(chan core.Event, 8)
			var message func(string, string)
			var reasoning func()
			var finish func()
			if backend == "app_server" {
				s := &appServerSession{events: events, currentTurn: "turn"}
				message = func(phase, text string) {
					s.handleItemCompleted(map[string]any{"type": "agentMessage", "phase": phase, "text": text})
				}
				reasoning = func() {
					s.handleItemCompleted(map[string]any{"type": "reasoning", "summary": []any{"planning"}})
				}
				finish = s.flushPendingAsText
			} else {
				s := &codexSession{ctx: context.Background(), events: events}
				message = func(phase, text string) {
					s.handleItemCompleted(map[string]any{"item": map[string]any{
						"type": "agent_message", "phase": phase, "text": text,
					}})
				}
				reasoning = func() {
					s.handleItemCompleted(map[string]any{"item": map[string]any{
						"type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "planning"}},
					}})
				}
				finish = s.flushPendingAsText
			}

			message("commentary", "Progress requested via /ps")
			select {
			case event := <-events:
				if event.Type != core.EventCommentary || event.Content != "Progress requested via /ps" {
					t.Fatalf("progress = %+v, want immediate commentary", event)
				}
			default:
				t.Fatal("commentary was buffered until a tool or turn completion")
			}
			reasoning()
			message("final_answer", "Done")
			finish()
			for i, want := range []core.EventType{core.EventThinking, core.EventText} {
				select {
				case event := <-events:
					if event.Type != want {
						t.Fatalf("event %d = %+v, want %s", i, event, want)
					}
					if i == 1 && event.Content != "Done" {
						t.Fatalf("final answer includes progress: %+v", event)
					}
				default:
					t.Fatalf("missing event %d (%s)", i, want)
				}
			}
			if len(events) != 0 {
				t.Fatalf("commentary was emitted again at completion: %d events", len(events))
			}
		})
	}
}

func TestCodexCommentary_LegacyMessagesAreNotThinking(t *testing.T) {
	for _, backend := range []string{"app_server", "exec"} {
		t.Run(backend, func(t *testing.T) {
			events := make(chan core.Event, 8)
			if backend == "app_server" {
				s := &appServerSession{events: events}
				s.handleItemCompleted(map[string]any{"type": "agentMessage", "text": "Working"})
				s.handleItemStarted(map[string]any{"type": "commandExecution", "command": "pwd"})
			} else {
				s := &codexSession{ctx: context.Background(), events: events}
				s.handleItemCompleted(map[string]any{"item": map[string]any{"type": "agent_message", "text": "Working"}})
				s.handleItemStarted(map[string]any{"item": map[string]any{"type": "command_execution", "command": "pwd"}})
			}
			event := <-events
			if event.Type != core.EventCommentary || event.Content != "Working" {
				t.Fatalf("intermediate message = %+v, want commentary, not thinking", event)
			}
			if event := <-events; event.Type != core.EventToolUse {
				t.Fatalf("next event = %+v, want tool use", event)
			}
		})
	}
}

func TestCodexCommentary_AnswerPhasesSurviveToolBoundaries(t *testing.T) {
	for _, backend := range []string{"app_server", "exec"} {
		t.Run(backend, func(t *testing.T) {
			events := make(chan core.Event, 16)
			var message func(string, string)
			var tool, finish func()
			if backend == "app_server" {
				s := &appServerSession{events: events}
				message = func(phase, text string) {
					s.handleItemCompleted(map[string]any{"type": "agentMessage", "phase": phase, "text": text})
				}
				tool = func() { s.handleItemStarted(map[string]any{"type": "commandExecution", "command": "pwd"}) }
				finish = s.flushPendingAsText
			} else {
				s := &codexSession{ctx: context.Background(), events: events}
				message = func(phase, text string) {
					s.handleItemCompleted(map[string]any{"item": map[string]any{"type": "agent_message", "phase": phase, "text": text}})
				}
				tool = func() {
					s.handleItemStarted(map[string]any{"item": map[string]any{"type": "command_execution", "command": "pwd"}})
				}
				finish = s.flushPendingAsText
			}
			message("partial_answer", "First part")
			tool()
			message("final_answer", "Last part")
			tool()
			finish()
			for i, want := range []core.EventType{core.EventText, core.EventToolUse, core.EventText, core.EventToolUse} {
				select {
				case ev := <-events:
					if ev.Type != want {
						t.Fatalf("event %d = %+v, want %s", i, ev, want)
					}
					if i == 0 && ev.Content != "First part" || i == 2 && ev.Content != "Last part" {
						t.Fatalf("answer lost: %+v", ev)
					}
				default:
					t.Fatalf("missing event %d", i)
				}
			}
			if len(events) != 0 {
				t.Fatalf("answer duplicated at completion: %d", len(events))
			}
		})
	}
}
