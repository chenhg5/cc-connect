package fakemodel

import (
	"encoding/json"
	"net/http"
	"strings"
)

// OpenAI Responses wire format (POST /v1/responses), as used by Codex.
//
// A conversation arrives as a flat list of input items: messages (with
// input_text / output_text / input_image content), function_call items the
// model made earlier, and function_call_output items carrying tool results.
// The fake answers with the same turnPlan as the Anthropic side, rendered as
// Responses streaming events.

type responsesRequest struct {
	Model  string            `json:"model"`
	Stream bool              `json:"stream"`
	Input  []responsesItem   `json:"input"`
	Tools  []json.RawMessage `json:"tools"`
}

type responsesItem struct {
	Type    string          `json:"type"`
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	CallID  string          `json:"call_id"`
	Output  json.RawMessage `json:"output"`
}

type responsesContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// codexShellTool is Codex's shell tool. A "FAKE:tool Bash <cmd>" directive
// becomes a call to it with {"cmd": <cmd>}.
const codexShellTool = "exec_command"

func (s *Server) handleResponses(w http.ResponseWriter, body []byte) {
	var req responsesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "fakemodel: bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	rec := describeResponses(req)
	s.mu.Lock()
	s.requests = append(s.requests, rec)
	s.mu.Unlock()

	if msg, failed := failDirective(rec); failed {
		writeError(w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{"type": "invalid_request_error", "code": "fakemodel_failure", "message": msg},
		})
		return
	}
	p := plan(rec, func() string { return responsesHistory(req.Input) })
	if !req.Stream {
		writeJSON(w, s.responseObject(req.Model, s.outputItems(p), "completed"))
		return
	}
	s.streamResponses(w, req, p)
}

// describeResponses extracts the newest user turn: the items after the last
// assistant message, or a trailing tool result.
func describeResponses(req responsesRequest) Request {
	rec := Request{Wire: "openai", Path: "/v1/responses", Stream: req.Stream, Model: req.Model, Messages: len(req.Input), Tools: len(req.Tools)}
	if n := len(req.Input); n > 0 && req.Input[n-1].Type == "function_call_output" {
		rec.ToolResult = strings.TrimSpace(functionOutputText(req.Input[n-1].Output))
		return rec
	}
	var texts []string
	for i := len(req.Input) - 1; i >= 0; i-- {
		it := req.Input[i]
		if it.Type == "message" && it.Role == "assistant" {
			break
		}
		if it.Type != "message" || it.Role != "user" {
			continue
		}
		for _, c := range responsesContents(it.Content) {
			switch c.Type {
			case "input_text":
				texts = append([]string{c.Text}, texts...)
			case "input_image":
				rec.Images++
			}
		}
	}
	rec.UserText = strings.Join(texts, "\n")
	if m := directiveRE.FindAllString(rec.UserText, -1); len(m) > 0 {
		rec.Directive = strings.TrimSpace(m[len(m)-1])
	}
	return rec
}

func responsesContents(raw json.RawMessage) []responsesContent {
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return []responsesContent{{Type: "input_text", Text: str}}
	}
	var cs []responsesContent
	_ = json.Unmarshal(raw, &cs)
	return cs
}

// functionOutputText reads a function_call_output's output, which is either a
// string or a list of content parts.
func functionOutputText(raw json.RawMessage) string {
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return str
	}
	var parts []string
	for _, c := range responsesContents(raw) {
		if c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// responsesHistory returns the conversation before the newest user message,
// reduced like history(): user directives and assistant answers only.
func responsesHistory(items []responsesItem) string {
	last := -1
	for i, it := range items {
		if it.Type == "message" && it.Role == "user" {
			last = i
		}
	}
	var parts []string
	for i, it := range items {
		if i >= last || it.Type != "message" {
			continue
		}
		for _, c := range responsesContents(it.Content) {
			parts = appendTurnText(parts, it.Role, c.Text)
		}
	}
	return strings.Join(parts, " | ")
}

// outputItems renders a plan as Responses output items.
func (s *Server) outputItems(p turnPlan) []map[string]any {
	if p.tool != "" {
		in := map[string]any{"cmd": p.toolIn}
		if p.escalate {
			in["sandbox_permissions"] = "require_escalated"
			in["justification"] = "fakemodel needs to write in the workspace"
		}
		args, _ := json.Marshal(in)
		return []map[string]any{{
			"type": "function_call", "id": s.nextID("fc"), "call_id": s.nextID("call"),
			"name": codexShellTool, "arguments": string(args), "status": "completed",
		}}
	}
	return []map[string]any{{
		"type": "message", "id": s.nextID("msg"), "role": "assistant", "status": "completed",
		"content": []map[string]any{{"type": "output_text", "text": strings.Join(p.text, ""), "annotations": []any{}}},
	}}
}

func (s *Server) responseObject(model string, output []map[string]any, status string) map[string]any {
	return map[string]any{
		"id": s.nextID("resp"), "object": "response", "model": model, "status": status, "output": output,
		"usage": map[string]any{
			"input_tokens": 10, "output_tokens": 1, "total_tokens": 11,
			"input_tokens_details":  map[string]any{"cached_tokens": 0},
			"output_tokens_details": map[string]any{"reasoning_tokens": 0},
		},
	}
}

func (s *Server) streamResponses(w http.ResponseWriter, req responsesRequest, p turnPlan) {
	sse, ok := newSSE(w)
	if !ok {
		http.Error(w, "fakemodel: streaming unsupported", http.StatusInternalServerError)
		return
	}
	created := s.responseObject(req.Model, []map[string]any{}, "in_progress")
	delete(created, "usage")
	sse.data(map[string]any{"type": "response.created", "response": created})

	items := s.outputItems(p)
	item := items[0]
	if p.tool == "" {
		started := map[string]any{"type": "message", "id": item["id"], "role": "assistant", "status": "in_progress", "content": []any{}}
		sse.data(map[string]any{"type": "response.output_item.added", "output_index": 0, "item": started})
		for i, c := range p.text {
			if i > 0 && p.gap > 0 {
				sleep(p.gap)
			}
			sse.data(map[string]any{"type": "response.output_text.delta", "item_id": item["id"],
				"output_index": 0, "content_index": 0, "delta": c})
		}
	} else {
		sse.data(map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item})
	}
	sse.data(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})
	sse.data(map[string]any{"type": "response.completed", "response": s.responseObject(req.Model, items, "completed")})
}
