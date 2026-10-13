package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ExecutionBlock is one intact public message or tool invocation and its result.
// Internal reasoning is deliberately not represented here.
type ExecutionBlock struct {
	ID, Key, Text, Tool, Command, Output, Status string
	StartedAt                                    time.Time
	DurationMS                                   int64
	ExitCode                                     *int
	Done                                         bool
}

type ExecutionCard interface {
	UpdateExecution(context.Context, []ExecutionBlock) error
	FinishExecution(context.Context, []ExecutionBlock, string, CardStatus) error
}

type nativeWorkflowCard struct {
	inner     InteractiveStreamingCard
	sink      ExecutionCard
	blocks    []ExecutionBlock
	mention   string
	omitFinal bool
	notice    string
}

func (c *nativeWorkflowCard) ObserveExecution(ctx context.Context, e Event) error {
	switch e.Type {
	case EventProgress:
		if e.Content != "" {
			found := false
			for i := range c.blocks {
				if e.TextID != "" && c.blocks[i].Tool == "" && c.blocks[i].ID == e.TextID {
					c.blocks[i].Text, c.blocks[i].Done = e.Content, true
					found = true
					break
				}
			}
			if !found {
				c.blocks = append(c.blocks, ExecutionBlock{ID: e.TextID, Text: e.Content, Done: true})
			}
		}
	case EventText:
		if e.Content == "" {
			return nil
		}
		n := len(c.blocks)
		if n > 0 && c.blocks[n-1].Tool == "" && !c.blocks[n-1].Done && (e.TextID == "" || c.blocks[n-1].ID == e.TextID) {
			c.blocks[n-1].Text += e.Content
		} else {
			if n > 0 && c.blocks[n-1].Tool == "" {
				c.blocks[n-1].Done = true
			}
			c.blocks = append(c.blocks, ExecutionBlock{ID: e.TextID, Text: e.Content})
		}
	case EventToolUse:
		for i := range c.blocks {
			if c.blocks[i].Tool == "" {
				c.blocks[i].Done = true
			}
		}
		c.blocks = append(c.blocks, ExecutionBlock{ID: e.ToolID, Tool: executionToolName(e), Command: executionToolInput(e), StartedAt: time.Now()})
	case EventToolResult:
		found := -1
		for i := range c.blocks {
			b := c.blocks[i]
			if b.Tool != "" && ((e.ToolID != "" && b.ID == e.ToolID) || (e.ToolID == "" && !b.Done && b.Tool == executionToolName(e))) {
				if found != -1 && e.ToolID == "" {
					found = -2
					break
				}
				found = i
			}
		}
		output := e.ToolResult
		if output == "" {
			output = e.Content
		}
		if (e.ToolSuccess != nil && !*e.ToolSuccess) || (e.ToolExitCode != nil && *e.ToolExitCode != 0) {
			e.ToolStatus = "failed"
		}
		if found >= 0 {
			b := &c.blocks[found]
			if e.ToolResultDelta {
				b.Output += output
			} else if output != "" || e.ToolResultPresent {
				b.Output = output
			}
			if !e.ToolUpdate || e.ToolStatus != "" {
				b.Status, b.Done = e.ToolStatus, e.ToolStatus != "in_progress" && e.ToolStatus != "pending" && e.ToolStatus != "running"
			}
			if e.ToolUpdate && (e.ToolInput != "" || len(e.ToolInputRaw) > 0) {
				b.Command = executionToolInput(e)
			}
			if kind, _ := e.Metadata["tool_kind"].(string); e.ToolUpdate && kind != "" && kind != "other" {
				b.Tool = executionToolName(e)
			}
			if e.ToolExitCode != nil {
				b.ExitCode = e.ToolExitCode
			}
			if c.blocks[found].Done && !c.blocks[found].StartedAt.IsZero() {
				c.blocks[found].DurationMS = time.Since(c.blocks[found].StartedAt).Milliseconds()
			}
		} else {
			// Ambiguous legacy results are kept separately, never attached to the
			// wrong concurrent invocation just because its tool name matches.
			c.blocks = append(c.blocks, ExecutionBlock{ID: e.ToolID, Tool: e.ToolName, Output: output, Status: e.ToolStatus, Done: true})
		}
	case EventPermissionRequest:
		for i := range c.blocks {
			if c.blocks[i].Tool == "" {
				c.blocks[i].Done = true
			}
		}
	case EventPlan:
		if len(e.Plan) == 0 {
			return nil
		}
		text := ""
		for _, step := range e.Plan {
			text += fmt.Sprintf("- %s · %s\n", step.Step, step.Status)
		}
		c.blocks = append(c.blocks, ExecutionBlock{Text: text, Done: true})
	default:
		return nil
	}
	if len(c.blocks) == 1 && c.blocks[0].Tool == "" && strings.HasPrefix("NO_REPLY", strings.TrimSpace(c.blocks[0].Text)) {
		return nil
	}
	// A lone streamed answer is delivered by the final handoff. Buffer it
	// until a tool or public progress makes it part of an execution transcript,
	// rather than creating a process card that will become empty at completion.
	if len(c.blocks) == 1 && c.blocks[0].Tool == "" && !c.blocks[0].Done {
		return nil
	}
	return c.sink.UpdateExecution(ctx, c.blocks)
}

// Normalize semantic categories, never agent names or human-written tool titles.
func executionToolName(e Event) string {
	kind, _ := e.Metadata["tool_kind"].(string)
	if kind == "" || kind == "other" {
		kind = e.ToolName
	}
	switch strings.ToLower(kind) {
	case "bash", "shell", "terminal", "execute", "commandexecution", "command_execution":
		return "command_execution"
	case "read", "read_file":
		return "read_file"
	case "edit", "write", "patch", "filechange", "file_change":
		return "file_change"
	case "websearch", "web_search":
		return "web_search"
	default:
		return e.ToolName
	}
}

func executionToolInput(e Event) string {
	// Shell commands remain readable code; transport JSON belongs to structured tools.
	for _, key := range []string{"command", "cmd"} {
		if command, ok := e.ToolInputRaw[key].(string); ok && command != "" {
			return command
		}
	}
	if len(e.ToolInputRaw) > 0 {
		if raw, err := json.MarshalIndent(e.ToolInputRaw, "", "  "); err == nil {
			return string(raw)
		}
	}
	return e.ToolInput
}

func (c *nativeWorkflowCard) Update(context.Context, string) error { return nil }
func (c *nativeWorkflowCard) Failed() bool                         { return c.inner.Failed() }
func (c *nativeWorkflowCard) SeparateFinalAnswer() bool            { return true }
func (c *nativeWorkflowCard) SetPrompt(ctx context.Context, p *Card) error {
	return c.inner.SetPrompt(ctx, p)
}
func (c *nativeWorkflowCard) SetNotice(ctx context.Context, s string) error {
	c.notice = s
	return c.inner.SetNotice(ctx, s)
}
func (c *nativeWorkflowCard) Finish(ctx context.Context, _ string, status CardStatus) error {
	if status == CardStatusDone {
		status = CardStatusStopped
	}
	answer := c.notice
	if answer != "" && c.mention != "" {
		answer = c.mention + "\n" + answer
	}
	return c.sink.FinishExecution(ctx, c.blocks, answer, status)
}
func (c *nativeWorkflowCard) Finalize(ctx context.Context, answer string) error {
	return c.FinalizeWithAnswer(ctx, "", answer)
}
func (c *nativeWorkflowCard) FinalizeWithAnswer(ctx context.Context, _, answer string) error {
	// The last streamed assistant segment is replaced by its authoritative final
	// response, while all preceding public progress and tools remain in order.
	if n := len(c.blocks); n > 0 && c.blocks[n-1].Tool == "" && !c.blocks[n-1].Done {
		c.blocks = c.blocks[:n-1]
	}
	if c.omitFinal {
		answer = ""
	}
	if answer != "" && c.mention != "" {
		answer = c.mention + "\n" + answer
	}
	return c.sink.FinishExecution(ctx, c.blocks, answer, CardStatusDone)
}
