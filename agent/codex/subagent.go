package codex

import (
	"encoding/json"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// Owned by the session's event reader. A thread keeps its card identity across
// turns, including work that continues after the parent's turn has ended.
type codexSubagents struct {
	threads map[string]*codexSubagent
}

type codexSubagent struct {
	title, kind string
	finished    bool
}

func (c *codexSubagents) has(id string) bool { return c.threads[id] != nil }

func (c *codexSubagents) start(id, title, kind, prompt string, emit func(core.Event)) {
	if id == "" {
		return
	}
	if c.threads == nil {
		c.threads = make(map[string]*codexSubagent)
	}
	a := c.threads[id]
	if a != nil && !a.finished {
		return
	}
	if a == nil {
		a = &codexSubagent{title: title, kind: kind}
		if a.title == "" {
			a.title = id
		}
		c.threads[id] = a
	}
	a.finished = false
	c.emit(core.SubagentEvent{ID: id, Phase: core.SubagentStarted, Title: a.title, Kind: a.kind, Prompt: prompt, Background: true}, emit)
}

func (c *codexSubagents) finish(id, status, summary string, emit func(core.Event)) {
	a := c.threads[id]
	if a == nil || a.finished {
		return
	}
	a.finished = true
	c.emit(core.SubagentEvent{ID: id, Phase: core.SubagentFinished, Status: status, Summary: summary}, emit)
}

func (c *codexSubagents) emit(ev core.SubagentEvent, emit func(core.Event)) {
	emit(core.Event{Type: core.EventSubagent, Subagent: &ev})
}

// A completed collaboration tool call is not a completed subagent: spawning
// returns while the child is still running. Only the per-agent state ends it.
func (c *codexSubagents) collab(item map[string]any, emit func(core.Event)) {
	prompt, _ := item["prompt"].(string)
	tool, _ := item["tool"].(string)
	ids, _ := item["receiverThreadIds"].([]any)
	states, _ := item["agentsStates"].(map[string]any)
	for _, rawID := range ids {
		id, _ := rawID.(string)
		if !c.has(id) {
			c.start(id, "", "", prompt, emit)
		}
		state, _ := states[id].(map[string]any)
		status, _ := state["status"].(string)
		message, _ := state["message"].(string)
		switch status {
		case "completed":
			c.finish(id, "completed", message, emit)
		case "errored", "notFound", "not_found":
			c.finish(id, "failed", message, emit)
		case "interrupted", "shutdown":
			c.finish(id, "interrupted", message, emit)
		case "running", "pendingInit", "pending_init":
			if tool == "sendInput" || tool == "send_input" || tool == "resumeAgent" || tool == "followupTask" {
				c.start(id, "", "", prompt, emit)
			}
		}
	}
}

func (c *codexSubagents) activity(item map[string]any, emit func(core.Event)) {
	id, _ := item["agentThreadId"].(string)
	kind, _ := item["kind"].(string)
	title, _ := item["agentPath"].(string)
	switch kind {
	case "started", "interacted":
		c.start(id, title, "", "", emit)
	case "completed":
		c.finish(id, "completed", "", emit)
	case "interrupted":
		c.finish(id, "interrupted", "", emit)
	}
}

// Route only descendants of this session. Foreign lifecycle and usage events
// must never end the parent's turn, clear its answer or replace its context.
func (s *appServerSession) handleSubagentNotification(method string, raw json.RawMessage) bool {
	if method == "thread/started" {
		var n struct {
			Thread struct {
				ID, ParentThreadID, AgentNickname, AgentRole, Preview string
				Source                                                struct {
					SubAgent struct {
						ThreadSpawn struct {
							ParentID string `json:"parent_thread_id"`
						} `json:"thread_spawn"`
					}
				}
			}
		}
		if json.Unmarshal(raw, &n) == nil {
			parent := n.Thread.ParentThreadID
			if parent == "" {
				parent = n.Thread.Source.SubAgent.ThreadSpawn.ParentID
			}
			if parent != "" && (parent == s.CurrentSessionID() || s.subagents.has(parent)) {
				s.subagents.start(n.Thread.ID, n.Thread.AgentNickname, n.Thread.AgentRole, n.Thread.Preview, s.emit)
			}
		}
		return true
	}
	var n struct {
		ThreadID string
		Item     map[string]any
		Turn     struct {
			Status string
			Error  *struct{ Message string }
		}
		Status struct{ Type string }
	}
	if json.Unmarshal(raw, &n) != nil || n.ThreadID == "" || n.ThreadID == s.CurrentSessionID() || s.CurrentSessionID() == "" {
		return false
	}
	if !s.subagents.has(n.ThreadID) {
		return true
	}
	switch method {
	case "turn/started":
		s.subagents.start(n.ThreadID, "", "", "", s.emit)
	case "item/started", "item/completed":
		s.handleSubagentItem(n.ThreadID, method == "item/started", n.Item)
	case "turn/completed":
		status, summary := n.Turn.Status, ""
		if n.Turn.Error != nil {
			status, summary = "failed", n.Turn.Error.Message
		}
		s.subagents.finish(n.ThreadID, status, summary, s.emit)
	case "thread/status/changed":
		if n.Status.Type == "idle" {
			s.subagents.finish(n.ThreadID, "completed", "", s.emit)
		}
	}
	return true
}

func (s *appServerSession) handleSubagentItem(id string, started bool, item map[string]any) {
	typ, _ := item["type"].(string)
	if typ == "collabAgentToolCall" {
		s.subagents.collab(item, s.emit)
		return
	}
	if typ == "subAgentActivity" {
		s.subagents.activity(item, s.emit)
		return
	}
	var step core.ProgressCardEntry
	if started {
		step.Kind = core.ProgressEntryToolUse
		switch typ {
		case "commandExecution":
			step.Tool, step.Text = "Bash", stringField(item, "command")
		case "mcpToolCall":
			step.Tool, step.Text = "MCP", stringField(item, "server")+":"+stringField(item, "tool")+"\n"+appServerJSON(item["arguments"])
		case "dynamicToolCall":
			step.Tool, step.Text = stringField(item, "tool"), appServerJSON(item["arguments"])
		case "webSearch":
			step.Tool, step.Text = "WebSearch", stringField(item, "query")
		case "fileChange":
			step.Tool, step.Text = "Patch", appServerJSON(item["changes"])
		default:
			return
		}
	} else {
		switch typ {
		case "agentMessage":
			step.Kind, step.Text = core.ProgressEntryInfo, stringField(item, "text")
		case "reasoning":
			step.Kind, step.Text = core.ProgressEntryThinking, appServerReasoningText(item)
		default:
			return
		}
	}
	if strings.TrimSpace(step.Text) != "" {
		s.subagents.emit(core.SubagentEvent{ID: id, Phase: core.SubagentStep, Step: step}, s.emit)
	}
}

func stringField(item map[string]any, key string) string { v, _ := item[key].(string); return v }

// exec's JSONL protocol uses snake_case for the same collaboration payload.
func (cs *codexSession) handleCollabItem(item map[string]any) {
	normalized := map[string]any{
		"tool": item["tool"], "prompt": item["prompt"],
		"receiverThreadIds": item["receiver_thread_ids"], "agentsStates": item["agents_states"],
	}
	cs.subagents.collab(normalized, func(e core.Event) {
		select {
		case cs.events <- e:
		case <-cs.ctx.Done():
		}
	})
}
