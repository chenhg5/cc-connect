package core

import (
	"log/slog"
	"strings"
)

// Match invocation IDs first; tool names alone cannot distinguish parallel Bash
// calls. Older adapters without IDs retain their existing name-based matching.
func findToolStep(steps []ToolStep, itemID, name string) int {
	for i := len(steps) - 1; i >= 0; i-- {
		step := steps[i]
		if step.Kind == ToolStepKindThinking {
			continue
		}
		if itemID != "" && step.ItemID != "" {
			if step.ItemID == itemID {
				return i
			}
			continue
		}
		if !step.Done && (step.Name == "" || step.Name == name) {
			return i
		}
	}
	return -1
}

func mergeLiveToolOutput(steps []ToolStep, event Event, maxLen int) []ToolStep {
	name := event.ToolName
	if name == "" {
		name = "Tool"
	}
	i := findToolStep(steps, event.ItemID, name)
	if i < 0 {
		steps = append(steps, ToolStep{ItemID: event.ItemID, Kind: ToolStepKindTool, Name: name})
		i = len(steps) - 1
	}
	if steps[i].Done {
		return steps // Late transport deltas cannot undo completion.
	}
	if event.ItemID != "" {
		steps[i].ItemID = event.ItemID
	}
	steps[i].Status = "inProgress"
	// Show the newest output within the configured display limit; keep a hard
	// bound when truncation is disabled so a long-running command cannot grow
	// this live UI buffer indefinitely. The daemon retains the full output.
	if maxLen <= 0 {
		maxLen = 8192
	}
	text := []rune(steps[i].Result + event.Content)
	if len(text) > maxLen {
		text = text[len(text)-maxLen:]
	}
	steps[i].Result = string(text)
	return steps
}

func (e *Engine) updateLiveToolCard(p Platform, reply any, handle *any, steps []ToolStep, markdown, footer string) bool {
	rich, ok := p.(RichCardSupporter)
	if !ok || e.display.CardMode != "rich" {
		return false
	}
	if resolver, ok := p.(RichCardMarkdownResolver); ok && strings.TrimSpace(markdown) != "" {
		markdown = resolver.ResolveRichCardMarkdown(e.ctx, markdown, false)
	}
	card := rich.BuildRichCard(CardStatusWorking, "", steps, markdown, true, footer)
	if *handle == nil {
		if starter, ok := p.(PreviewStarter); ok {
			value, err := starter.SendPreviewStart(e.ctx, reply, card)
			if err == nil {
				*handle = value
				return true
			}
			slog.Warn("live tool output: create card", "error", err)
		}
	} else if updater, ok := p.(MessageUpdater); ok {
		if err := updater.UpdateMessage(e.ctx, *handle, card); err == nil {
			return true
		} else {
			slog.Warn("live tool output: update card", "error", err)
		}
	}
	return false
}
