// Package fakemodel is a scripted stand-in for a model API. It speaks two
// wire formats on one server:
//
//   - Anthropic Messages (POST /v1/messages), used by Claude Code
//   - OpenAI Responses (POST /v1/responses), used by Codex
//
// Real agent CLIs are pointed at it through a provider base_url, so blackbox
// tests run the real CLI and the real cc-connect without a model key, and
// every turn is deterministic.
//
// The script lives in the user's message: the fake looks for the last
// directive of the form "FAKE:<verb> <args>" in the newest user turn.
//
//	FAKE:reply <text>        answer with <text>
//	FAKE:slow <n> <word>     stream <word> n times, 300ms apart (streaming previews)
//	FAKE:tool Bash <command> ask to run <command> with the agent's shell tool
//	                         (Bash for Claude Code, exec_command for Codex); once
//	                         the tool result comes back, answer "TOOL_DONE: <result>"
//	FAKE:escalate <command>  like FAKE:tool Bash, but asks to run outside the
//	                         sandbox (Codex: sandbox_permissions=require_escalated,
//	                         which triggers an approval request); Claude Code
//	                         treats it as a plain Bash call
//	FAKE:recall              answer "HISTORY: " + the text of earlier turns
//	FAKE:fail <text>         fail the request with HTTP 400 and message <text>
//	                         (a model/provider outage, not retried by the CLIs)
//	(no directive)           answer "ECHO: <first line of the message>"
//
// Non-streaming side requests the CLI makes for itself (titles, summaries)
// get a short fixed answer. Every request is recorded for assertions.
package fakemodel

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Request is one recorded call to the fake.
type Request struct {
	// Wire is "anthropic" or "openai".
	Wire   string
	Path   string
	Stream bool
	Model  string
	// UserText is the text of the newest user message (all text blocks).
	UserText string
	// Directive is the FAKE: directive found in UserText ("" if none).
	Directive string
	// ToolResult is set when the newest user message carries a tool result.
	ToolResult string
	ToolError  bool
	// Images counts image blocks in the newest user message.
	Images int
	// Messages is the number of messages in the conversation sent.
	Messages int
	// Tools is the number of tool definitions sent.
	Tools int
}

// Server is a running fake. Create it with New and Close it when done.
type Server struct {
	srv *httptest.Server

	mu       sync.Mutex
	requests []Request
	seq      int
}

// New starts a fake on a loopback port.
func New() *Server {
	s := &Server{}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// URL is the base URL to give the agent as its provider base_url.
func (s *Server) URL() string { return s.srv.URL }

// Close stops the server.
func (s *Server) Close() { s.srv.Close() }

// Requests returns a snapshot of the recorded requests.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// TurnRequests returns only the streaming requests, i.e. the agent's
// conversation turns, not the CLI's own side requests.
func (s *Server) TurnRequests() []Request {
	var out []Request
	for _, r := range s.Requests() {
		if r.Stream {
			out = append(out, r)
		}
	}
	return out
}

func (s *Server) nextID(prefix string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return fmt.Sprintf("%s_fake_%d", prefix, s.seq)
}

// ── HTTP ─────────────────────────────────────────────────────────────────────

type apiRequest struct {
	Model    string            `json:"model"`
	Stream   bool              `json:"stream"`
	Messages []apiMessage      `json:"messages"`
	Tools    []json.RawMessage `json:"tools"`
}

type apiMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type apiBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
	ToolUseID string          `json:"tool_use_id"`
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.Contains(r.URL.Path, "count_tokens") {
		writeJSON(w, map[string]any{"input_tokens": 10})
		return
	}
	if strings.HasSuffix(r.URL.Path, "/responses") {
		s.handleResponses(w, body)
		return
	}
	if !strings.HasSuffix(r.URL.Path, "/v1/messages") {
		http.NotFound(w, r)
		return
	}

	var req apiRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "fakemodel: bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	rec := describe(r.URL.Path, req)
	s.mu.Lock()
	s.requests = append(s.requests, rec)
	s.mu.Unlock()

	if msg, failed := failDirective(rec); failed {
		writeError(w, http.StatusBadRequest, map[string]any{
			"type": "error", "error": map[string]any{"type": "invalid_request_error", "message": msg},
		})
		return
	}
	if !req.Stream {
		// The CLI's own side requests: keep them short and harmless.
		writeJSON(w, s.message(req.Model, []map[string]any{{"type": "text", "text": "ok"}}, "end_turn"))
		return
	}
	s.stream(w, req, rec)
}

// describe extracts what the fake needs from the newest user message.
func describe(path string, req apiRequest) Request {
	rec := Request{Wire: "anthropic", Path: path, Stream: req.Stream, Model: req.Model, Messages: len(req.Messages), Tools: len(req.Tools)}
	last := lastUser(req.Messages)
	if last == nil {
		return rec
	}
	var texts []string
	for _, b := range blocks(last.Content) {
		switch b.Type {
		case "text":
			texts = append(texts, b.Text)
		case "image":
			rec.Images++
		case "tool_result":
			rec.ToolResult = strings.TrimSpace(blockText(b.Content))
			rec.ToolError = b.IsError
		}
	}
	rec.UserText = strings.Join(texts, "\n")
	if m := directiveRE.FindAllString(rec.UserText, -1); len(m) > 0 {
		rec.Directive = strings.TrimSpace(m[len(m)-1])
	}
	return rec
}

var directiveRE = regexp.MustCompile(`FAKE:[a-z]+[^\n]*`)

func lastUser(msgs []apiMessage) *apiMessage {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return &msgs[i]
		}
	}
	return nil
}

func blocks(raw json.RawMessage) []apiBlock {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []apiBlock{{Type: "text", Text: s}}
	}
	var bs []apiBlock
	_ = json.Unmarshal(raw, &bs)
	return bs
}

func blockText(raw json.RawMessage) string {
	var parts []string
	for _, b := range blocks(raw) {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// history returns the conversation before the newest user message, reduced
// to what a test can assert on: the FAKE: directives the user sent and the
// assistant's answers. Anything the CLI injects on its own (environment
// blocks, system reminders, instructions) is left out.
func history(msgs []apiMessage) string {
	var parts []string
	for i := 0; i < len(msgs)-1; i++ {
		for _, b := range blocks(msgs[i].Content) {
			if b.Type == "text" {
				parts = appendTurnText(parts, msgs[i].Role, b.Text)
			}
		}
	}
	return strings.Join(parts, " | ")
}

// appendTurnText adds one text block of the history: a user block only
// through the directive it carries, an assistant block as it is.
func appendTurnText(parts []string, role, text string) []string {
	text = strings.TrimSpace(text)
	switch role {
	case "user":
		if m := directiveRE.FindAllString(text, -1); len(m) > 0 {
			return append(parts, strings.TrimSpace(m[len(m)-1]))
		}
	case "assistant":
		if text != "" {
			return append(parts, text)
		}
	}
	return parts
}

// ── Turn script ──────────────────────────────────────────────────────────────

// turnPlan is what the fake answers, independent of the wire format.
type turnPlan struct {
	text   []string      // text chunks (empty for a tool call)
	gap    time.Duration // delay between chunks
	tool   string        // tool name when the answer is a tool call
	toolIn string        // the command for the tool call
	// escalate asks for the command to run outside the sandbox, which is what
	// makes Codex request user approval.
	escalate bool
}

// plan decides the answer to a turn from the recorded request and the text
// of earlier turns.
func plan(rec Request, history func() string) turnPlan {
	if rec.ToolResult != "" || rec.ToolError {
		if rec.ToolError {
			return turnPlan{text: []string{"TOOL_ERROR: " + truncate(rec.ToolResult, 300)}}
		}
		return turnPlan{text: []string{"TOOL_DONE: " + truncate(rec.ToolResult, 300)}}
	}
	verb, args := splitDirective(rec.Directive)
	switch verb {
	case "reply":
		return turnPlan{text: []string{args}}
	case "slow":
		n, word := 5, "tick"
		if f := strings.Fields(args); len(f) > 0 {
			if v, err := strconv.Atoi(f[0]); err == nil && v > 0 && v <= 50 {
				n = v
			}
			if len(f) > 1 {
				word = f[1]
			}
		}
		chunks := make([]string, n)
		for i := range chunks {
			chunks[i] = word + " "
		}
		return turnPlan{text: chunks, gap: 300 * time.Millisecond}
	case "tool":
		name, input := "Bash", args
		if f := strings.SplitN(args, " ", 2); len(f) == 2 {
			name, input = f[0], f[1]
		}
		return turnPlan{tool: name, toolIn: input}
	case "escalate":
		return turnPlan{tool: "Bash", toolIn: args, escalate: true}
	case "recall":
		return turnPlan{text: []string{"HISTORY: " + truncate(history(), 2000)}}
	default:
		first := strings.TrimSpace(rec.UserText)
		if i := strings.IndexByte(first, '\n'); i >= 0 {
			first = first[:i]
		}
		return turnPlan{text: []string{"ECHO: " + truncate(first, 200)}}
	}
}

func (s *Server) stream(w http.ResponseWriter, req apiRequest, rec Request) {
	sse, ok := newSSE(w)
	if !ok {
		http.Error(w, "fakemodel: streaming unsupported", http.StatusInternalServerError)
		return
	}
	id := s.nextID("msg")
	sse.event("message_start", map[string]any{"type": "message_start", "message": map[string]any{
		"id": id, "type": "message", "role": "assistant", "model": req.Model, "content": []any{},
		"stop_reason": nil, "stop_sequence": nil, "usage": map[string]any{"input_tokens": 10, "output_tokens": 0},
	}})

	p := plan(rec, func() string { return history(req.Messages) })
	if p.tool != "" {
		sse.toolUse(0, s.nextID("toolu"), p.tool, map[string]any{"command": p.toolIn, "description": "fakemodel tool call"})
		sse.finish("tool_use")
		return
	}
	sse.text(0, p.text, p.gap)
	sse.finish("end_turn")
}

func splitDirective(d string) (verb, args string) {
	d = strings.TrimPrefix(d, "FAKE:")
	if d == "" {
		return "", ""
	}
	verb, args, _ = strings.Cut(d, " ")
	return verb, strings.TrimSpace(args)
}

func (s *Server) message(model string, content []map[string]any, stop string) map[string]any {
	return map[string]any{
		"id": s.nextID("msg"), "type": "message", "role": "assistant", "model": model,
		"content": content, "stop_reason": stop, "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": 10, "output_tokens": 1},
	}
}

// ── SSE ──────────────────────────────────────────────────────────────────────

type sseWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func newSSE(w http.ResponseWriter) (*sseWriter, bool) {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	return &sseWriter{w: w, f: f}, true
}

func (s *sseWriter) event(name string, data any) {
	b, _ := json.Marshal(data)
	_, _ = fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, b)
	s.f.Flush()
}

// data writes an event whose name is its own "type" field (Responses style).
func (s *sseWriter) data(d map[string]any) {
	name, _ := d["type"].(string)
	s.event(name, d)
}

func sleep(d time.Duration) { time.Sleep(d) }

func (s *sseWriter) text(index int, chunks []string, gap time.Duration) {
	s.event("content_block_start", map[string]any{"type": "content_block_start", "index": index,
		"content_block": map[string]any{"type": "text", "text": ""}})
	for i, c := range chunks {
		if i > 0 && gap > 0 {
			time.Sleep(gap)
		}
		s.event("content_block_delta", map[string]any{"type": "content_block_delta", "index": index,
			"delta": map[string]any{"type": "text_delta", "text": c}})
	}
	s.event("content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
}

func (s *sseWriter) toolUse(index int, id, name string, input map[string]any) {
	s.event("content_block_start", map[string]any{"type": "content_block_start", "index": index,
		"content_block": map[string]any{"type": "tool_use", "id": id, "name": name, "input": map[string]any{}}})
	b, _ := json.Marshal(input)
	s.event("content_block_delta", map[string]any{"type": "content_block_delta", "index": index,
		"delta": map[string]any{"type": "input_json_delta", "partial_json": string(b)}})
	s.event("content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
}

func (s *sseWriter) finish(stop string) {
	s.event("message_delta", map[string]any{"type": "message_delta",
		"delta": map[string]any{"stop_reason": stop, "stop_sequence": nil}, "usage": map[string]any{"output_tokens": 1}})
	s.event("message_stop", map[string]any{"type": "message_stop"})
}

// failDirective reports whether the turn asked the fake to fail, and the
// error message to return. Only the newest user turn counts, so a later tool
// result in the same conversation is not failed again.
func failDirective(rec Request) (string, bool) {
	if rec.ToolResult != "" || rec.ToolError {
		return "", false
	}
	verb, args := splitDirective(rec.Directive)
	if verb != "fail" {
		return "", false
	}
	if args == "" {
		args = "fakemodel induced failure"
	}
	return args, true
}

func writeError(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
