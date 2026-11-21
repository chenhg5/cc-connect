package core

import (
	"log/slog"
	"strings"
	"time"
)

// Owned by the single shared reader. A tool may finish after its model turn;
// this display state has no task timers and cannot interrupt a different turn.
type sharedToolTail struct {
	done       bool
	steps      []ToolStep
	handle     any
	lastUpdate time.Time
	pending    *Event
}

func isSharedToolEvent(event Event) bool {
	return event.Type == EventToolUse || event.Type == EventToolOutput || event.Type == EventToolResult
}

func (tail *sharedToolTail) observe(event Event, maxLen int) {
	switch event.Type {
	case EventToolUse:
		if findToolStep(tail.steps, event.ItemID, event.ToolName) < 0 {
			tail.steps = append(tail.steps, ToolStep{ItemID: event.ItemID, Kind: ToolStepKindTool, Name: event.ToolName, Summary: truncateIf(event.ToolInput, maxLen)})
		}
	case EventToolOutput:
		tail.steps = mergeLiveToolOutput(tail.steps, event, maxLen)
	case EventToolResult:
		result := event.ToolResult
		if result == "" {
			result = event.Content
		}
		tail.steps = mergeRichToolResult(tail.steps, event, truncateIf(strings.TrimSpace(result), maxLen), maxLen)
	}
}

func (e *Engine) showSharedToolTail(state *interactiveState, tail *sharedToolTail, event Event) {
	if !e.display.ToolMessages {
		return
	}
	if event.Type == EventToolOutput && time.Since(tail.lastUpdate) < 500*time.Millisecond {
		tail.pending = &event
		return
	}
	tail.pending = nil
	tail.lastUpdate = time.Now()
	e.renderSharedToolTail(state, tail, event)
}

func (e *Engine) renderSharedToolTail(state *interactiveState, tail *sharedToolTail, event Event) {
	state.mu.Lock()
	p, reply, workspace := state.platform, state.replyCtx, state.workspaceDir
	state.mu.Unlock()
	if rich, ok := p.(RichCardSupporter); ok && e.display.CardMode == "rich" {
		status := CardStatusDone
		for _, step := range tail.steps {
			if !step.Done {
				status = CardStatusWorking
				break
			}
		}
		// Separate tool-only card for commands that outlive the completed answer.
		// No text animation: this card is a command status display, not a model turn.
		card := rich.BuildRichCard(status, "", tail.steps, "", false, "")
		if tail.handle != nil {
			if updater, ok := p.(MessageUpdater); ok {
				if err := updater.UpdateMessage(e.ctx, tail.handle, card); err == nil {
					return
				} else {
					slog.Warn("shared late tool: update card", "turn_id", event.TurnID, "error", err)
				}
			}
		} else if starter, ok := p.(PreviewStarter); ok {
			if handle, err := starter.SendPreviewStart(e.ctx, reply, card); err == nil {
				tail.handle = handle
				return
			} else {
				slog.Warn("shared late tool: create card", "turn_id", event.TurnID, "error", err)
			}
		}
		// Rich-capable platforms still receive tool panels on a failed preview edit.
		e.sendRaw(p, reply, card)
		return
	}
	// Existing plain/legacy behavior remains structured tool progress. Live
	// stdout must never be treated as assistant prose or added to history.
	switch event.Type {
	case EventToolResult:
		result := event.ToolResult
		if result == "" {
			result = event.Content
		}
		text := e.formatToolResultEventFallback(event.ToolName, truncateIf(result, e.display.ToolMaxLen), event.ToolStatus, event.ToolExitCode, event.ToolSuccess)
		if text != "" {
			e.sendForWorkspace(p, reply, text, workspace)
		}
	case EventToolUse:
		e.sendForWorkspace(p, reply, "🔧 "+event.ToolName+"\n"+truncateIf(event.ToolInput, e.display.ToolMaxLen), workspace)
	}
}
