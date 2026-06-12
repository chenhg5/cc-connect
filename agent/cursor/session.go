package cursor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
)

// cursorSession manages multi-turn conversations with the Cursor Agent CLI.
// Each Send() launches a new `agent --print` process with --resume for continuity.
type cursorSession struct {
	cmd      string // CLI binary name
	workDir  string
	model    string
	mode     string
	extraEnv []string
	events   chan core.Event
	chatID   atomic.Value // stores string — Cursor chat/session ID
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	alive    atomic.Bool

	thinkingBuf strings.Builder // accumulate thinking deltas

	// Permission handling: each Send() creates a new process whose stdin is used
	// to respond to interaction_query permission requests.
	stdinMu  sync.Mutex
	stdin    io.WriteCloser // current process stdin; nil when no process is running

	pendingMu sync.Mutex
	pending   *pendingInteractionQuery // most recent unresolved interaction_query/request
}

// pendingInteractionQuery holds the info needed to write a response back to Cursor.
type pendingInteractionQuery struct {
	id        int    // query["id"] value from the original request
	queryType string // e.g. "webFetchRequestQuery", "shellRequestQuery"
}

func newCursorSession(ctx context.Context, cmd, workDir, model, mode, resumeID string, extraEnv []string) (*cursorSession, error) {
	sessionCtx, cancel := context.WithCancel(ctx)

	cs := &cursorSession{
		cmd:      cmd,
		workDir:  workDir,
		model:    model,
		mode:     mode,
		extraEnv: extraEnv,
		events:   make(chan core.Event, 64),
		ctx:      sessionCtx,
		cancel:   cancel,
	}
	cs.alive.Store(true)

	if resumeID != "" && resumeID != core.ContinueSession {
		cs.chatID.Store(resumeID)
	}

	return cs, nil
}

func (cs *cursorSession) Send(prompt string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if len(images) > 0 {
		slog.Warn("cursorSession: images not yet supported in CLI mode, ignoring")
	}
	if len(files) > 0 {
		filePaths := core.SaveFilesToDisk(cs.workDir, files)
		prompt = core.AppendFileRefs(prompt, filePaths)
	}
	if !cs.alive.Load() {
		return fmt.Errorf("session is closed")
	}

	chatID := cs.CurrentSessionID()
	isResume := chatID != ""

	args := []string{
		"--print",
		"--output-format", "stream-json",
	}

	switch cs.mode {
	case "force":
		args = append(args, "--force")
	case "plan":
		args = append(args, "--mode", "plan")
	case "ask":
		args = append(args, "--mode", "ask")
	}

	if isResume {
		args = append(args, "--resume", chatID)
	}
	if cs.model != "" {
		args = append(args, "--model", cs.model)
	}
	args = append(args, "--workspace", cs.workDir, "--", prompt)

	slog.Debug("cursorSession: launching", "resume", isResume, "args", core.RedactArgs(args))

	cmd := exec.CommandContext(cs.ctx, cs.cmd, args...)
	cmd.Dir = cs.workDir
	env := os.Environ()
	if len(cs.extraEnv) > 0 {
		env = core.MergeEnv(env, cs.extraEnv)
	}
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("cursorSession: stdout pipe: %w", err)
	}

	// Set up a stdin pipe so we can write interaction_query responses back to Cursor.
	// Without a connected stdin, Cursor gets EOF and auto-rejects all permission requests.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("cursorSession: stdin pipe: %w", err)
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return fmt.Errorf("cursorSession: start: %w", err)
	}

	cs.stdinMu.Lock()
	cs.stdin = stdin
	cs.stdinMu.Unlock()

	cs.wg.Add(1)
	go cs.readLoop(cmd, stdout, &stderrBuf)

	return nil
}

func (cs *cursorSession) readLoop(cmd *exec.Cmd, stdout io.ReadCloser, stderrBuf *bytes.Buffer) {
	defer cs.wg.Done()
	defer func() {
		// Close stdin so Cursor knows the input stream is finished.
		cs.stdinMu.Lock()
		if cs.stdin != nil {
			_ = cs.stdin.Close()
			cs.stdin = nil
		}
		cs.stdinMu.Unlock()
	}()
	defer func() {
		if err := cmd.Wait(); err != nil {
			stderrMsg := strings.TrimSpace(stderrBuf.String())
			if stderrMsg != "" {
				slog.Error("cursorSession: process failed", "error", err, "stderr", stderrMsg)
				evt := core.Event{Type: core.EventError, Error: fmt.Errorf("%s", stderrMsg)}
				select {
				case cs.events <- evt:
				case <-cs.ctx.Done():
					return
				}
			}
		}
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		slog.Debug("cursorSession: raw", "line", truncateStr(line, 500))

		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			slog.Debug("cursorSession: non-JSON line", "line", line)
			continue
		}

		cs.handleEvent(raw)
	}

	if err := scanner.Err(); err != nil {
		slog.Error("cursorSession: scanner error", "error", err)
		evt := core.Event{Type: core.EventError, Error: fmt.Errorf("read stdout: %w", err)}
		select {
		case cs.events <- evt:
		case <-cs.ctx.Done():
			return
		}
	}
}

func (cs *cursorSession) handleEvent(raw map[string]any) {
	eventType, _ := raw["type"].(string)

	switch eventType {
	case "system":
		cs.handleSystem(raw)

	case "user":
		// User echo — nothing to do

	case "thinking":
		cs.handleThinking(raw)

	case "assistant":
		cs.handleAssistant(raw)

	case "tool_call":
		cs.handleToolCall(raw)

	case "interaction_query":
		cs.handleInteractionQuery(raw)

	case "result":
		cs.handleResult(raw)

	default:
		slog.Debug("cursorSession: unhandled event", "type", eventType)
	}
}

func (cs *cursorSession) handleSystem(raw map[string]any) {
	if sid, ok := raw["session_id"].(string); ok && sid != "" {
		cs.chatID.Store(sid)
		slog.Debug("cursorSession: session init", "session_id", sid)

		model, _ := raw["model"].(string)
		evt := core.Event{Type: core.EventText, SessionID: sid, Content: "", ToolName: model}
		select {
		case cs.events <- evt:
		case <-cs.ctx.Done():
			return
		}
	}
}

func (cs *cursorSession) handleThinking(raw map[string]any) {
	subtype, _ := raw["subtype"].(string)
	switch subtype {
	case "delta":
		if text, _ := raw["text"].(string); text != "" {
			cs.thinkingBuf.WriteString(text)
		}
	default:
		text := cs.thinkingBuf.String()
		cs.thinkingBuf.Reset()
		if text != "" {
			evt := core.Event{Type: core.EventThinking, Content: text}
			select {
			case cs.events <- evt:
			case <-cs.ctx.Done():
				return
			}
		}
	}
}

func (cs *cursorSession) handleAssistant(raw map[string]any) {
	msg, ok := raw["message"].(map[string]any)
	if !ok {
		return
	}
	contentArr, ok := msg["content"].([]any)
	if !ok {
		return
	}
	for _, contentItem := range contentArr {
		item, ok := contentItem.(map[string]any)
		if !ok {
			continue
		}
		contentType, _ := item["type"].(string)
		if contentType == "text" {
			if text, ok := item["text"].(string); ok && text != "" {
				evt := core.Event{Type: core.EventText, Content: text}
				select {
				case cs.events <- evt:
				case <-cs.ctx.Done():
					return
				}
			}
		}
	}
}

func (cs *cursorSession) handleToolCall(raw map[string]any) {
	subtype, _ := raw["subtype"].(string)
	tc, _ := raw["tool_call"].(map[string]any)
	if tc == nil {
		return
	}

	if subtype == "started" {
		name, input := extractToolInfo(tc)
		if name != "" {
			evt := core.Event{Type: core.EventToolUse, ToolName: name, ToolInput: input}
			select {
			case cs.events <- evt:
			case <-cs.ctx.Done():
				return
			}
		}
	}
	// "completed" tool_call events contain results; we log but don't emit to chat
	if subtype == "completed" {
		name, _ := extractToolInfo(tc)
		slog.Debug("cursorSession: tool completed", "tool", name)
	}
}

func (cs *cursorSession) handleInteractionQuery(raw map[string]any) {
	subtype, _ := raw["subtype"].(string)
	if subtype != "request" {
		return
	}

	queryType, _ := raw["query_type"].(string)
	query, _ := raw["query"].(map[string]any)
	if query == nil {
		return
	}

	// query["id"] is a JSON number — decoded as float64 by Go's json package.
	idFloat, _ := query["id"].(float64)
	queryID := int(idFloat)

	toolName, inputStr := extractInteractionQueryInfo(queryType, query)
	if toolName == "" {
		// Unknown query type — deny immediately to unblock Cursor.
		cs.writeInteractionResponse(queryID, queryType, false, "unsupported query type")
		return
	}

	// Check skipApproval flag: some queries are pre-approved by Cursor itself.
	if skipApproval := extractSkipApproval(queryType, query); skipApproval {
		cs.writeInteractionResponse(queryID, queryType, true, "")
		return
	}

	// In non-default modes there is no interactive approval path; deny to unblock.
	if cs.mode != "default" {
		cs.writeInteractionResponse(queryID, queryType, false, "permission mode does not allow interactive approval")
		return
	}

	// Store pending query so RespondPermission can write the right response.
	cs.pendingMu.Lock()
	cs.pending = &pendingInteractionQuery{id: queryID, queryType: queryType}
	cs.pendingMu.Unlock()

	// Build a stable requestID that the engine passes back to RespondPermission.
	requestID := fmt.Sprintf("%s:%d", queryType, queryID)

	slog.Info("cursorSession: permission request", "request_id", requestID, "tool", toolName)
	evt := core.Event{
		Type:      core.EventPermissionRequest,
		RequestID: requestID,
		ToolName:  toolName,
		ToolInput: inputStr,
	}
	select {
	case cs.events <- evt:
	case <-cs.ctx.Done():
		return
	}
}

// writeInteractionResponse writes a Cursor interaction_query response JSON line to the
// current process stdin.  approved=true → {"approved":{}}; false → {"rejected":{"reason":...}}.
func (cs *cursorSession) writeInteractionResponse(queryID int, queryType string, approved bool, reason string) {
	// Derive the response field name from the request query type.
	// "webFetchRequestQuery" → "webFetchRequestResponse"
	// "shellRequestQuery"   → "shellRequestResponse"
	responseKey := strings.TrimSuffix(queryType, "Query") + "Response"

	var resultValue any
	if approved {
		resultValue = map[string]any{"approved": map[string]any{}}
	} else {
		resultValue = map[string]any{"rejected": map[string]any{"reason": reason}}
	}

	resp := map[string]any{
		"type":       "interaction_query",
		"subtype":    "response",
		"query_type": queryType,
		"response": map[string]any{
			"id":        queryID,
			responseKey: resultValue,
		},
	}

	b, err := json.Marshal(resp)
	if err != nil {
		slog.Error("cursorSession: marshal interaction response", "error", err)
		return
	}
	b = append(b, '\n')

	cs.stdinMu.Lock()
	defer cs.stdinMu.Unlock()
	if cs.stdin == nil {
		slog.Warn("cursorSession: stdin unavailable, cannot write interaction response")
		return
	}
	if _, err := cs.stdin.Write(b); err != nil {
		slog.Warn("cursorSession: write interaction response", "error", err)
	}
}

// extractSkipApproval returns true when Cursor marks a query as pre-approved.
func extractSkipApproval(queryType string, query map[string]any) bool {
	// The inner query object is nested under the queryType key.
	inner, _ := query[queryType].(map[string]any)
	if inner == nil {
		return false
	}
	skip, _ := inner["skipApproval"].(bool)
	return skip
}

func extractInteractionQueryInfo(queryType string, query map[string]any) (string, string) {
	switch queryType {
	case "webFetchRequestQuery":
		if inner, ok := query["webFetchRequestQuery"].(map[string]any); ok {
			if args, ok := inner["args"].(map[string]any); ok {
				url, _ := args["url"].(string)
				return "WebFetch", url
			}
		}
	case "shellRequestQuery":
		if inner, ok := query["shellRequestQuery"].(map[string]any); ok {
			if args, ok := inner["args"].(map[string]any); ok {
				cmd, _ := args["command"].(string)
				return "Bash", cmd
			}
		}
	}

	name := strings.TrimSuffix(queryType, "RequestQuery")
	name = strings.TrimSuffix(name, "Query")
	if name == "" {
		name = queryType
	}
	return name, ""
}

// extractToolInfo parses the nested tool_call structure from Cursor's stream-json.
// Tool calls can be shellToolCall, readToolCall, editToolCall, etc.
func extractToolInfo(tc map[string]any) (name string, input string) {
	toolTypes := []struct {
		key      string
		toolName string
	}{
		{"shellToolCall", "Bash"},
		{"readToolCall", "Read"},
		{"editToolCall", "Edit"},
		{"writeToolCall", "Write"},
		{"listToolCall", "List"},
		{"searchToolCall", "Search"},
		{"grepToolCall", "Grep"},
		{"globToolCall", "Glob"},
		{"webFetchToolCall", "WebFetch"},
	}

	for _, tt := range toolTypes {
		if call, ok := tc[tt.key].(map[string]any); ok {
			name = tt.toolName
			input = extractToolInput(name, call)
			return
		}
	}

	// Generic: try "description" field at top level
	if desc, ok := tc["description"].(string); ok && desc != "" {
		return "Tool", truncateStr(desc, 200)
	}

	return "", ""
}

func extractToolInput(toolName string, call map[string]any) string {
	args, _ := call["args"].(map[string]any)
	if args == nil {
		if desc, ok := call["description"].(string); ok {
			return desc
		}
		return ""
	}

	switch toolName {
	case "Bash":
		if cmd, ok := args["command"].(string); ok {
			return cmd
		}
	case "Read":
		if p, ok := args["path"].(string); ok {
			return p
		}
	case "Edit", "Write":
		if p, ok := args["path"].(string); ok {
			return p
		}
		if p, ok := args["filePath"].(string); ok {
			return p
		}
	case "Grep":
		if p, ok := args["pattern"].(string); ok {
			return p
		}
	case "Glob":
		if p, ok := args["pattern"].(string); ok {
			return p
		}
	}

	if desc, ok := call["description"].(string); ok && desc != "" {
		return desc
	}

	b, _ := json.Marshal(args)
	return string(b)
}

func (cs *cursorSession) handleResult(raw map[string]any) {
	var content string
	if result, ok := raw["result"].(string); ok {
		content = result
	}
	if sid, ok := raw["session_id"].(string); ok && sid != "" {
		cs.chatID.Store(sid)
	}
	evt := core.Event{Type: core.EventResult, Content: content, SessionID: cs.CurrentSessionID(), Done: true}
	select {
	case cs.events <- evt:
	case <-cs.ctx.Done():
		return
	}
}

// RespondPermission writes the user's approval/denial decision back to the Cursor Agent
// CLI via stdin.  Cursor is blocked waiting for this response when in default mode.
func (cs *cursorSession) RespondPermission(_ string, result core.PermissionResult) error {
	cs.pendingMu.Lock()
	pq := cs.pending
	cs.pending = nil
	cs.pendingMu.Unlock()

	if pq == nil {
		// No interaction query is pending — nothing to respond to.
		return nil
	}

	approved := result.Behavior == "allow"
	reason := result.Message
	if !approved && reason == "" {
		reason = "User denied"
	}
	cs.writeInteractionResponse(pq.id, pq.queryType, approved, reason)
	return nil
}

func (cs *cursorSession) Events() <-chan core.Event {
	return cs.events
}

func (cs *cursorSession) CurrentSessionID() string {
	v, _ := cs.chatID.Load().(string)
	return v
}

func (cs *cursorSession) Alive() bool {
	return cs.alive.Load()
}

func (cs *cursorSession) Close() error {
	cs.alive.Store(false)
	cs.cancel()
	done := make(chan struct{})
	go func() {
		cs.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		close(cs.events)
	case <-time.After(8 * time.Second):
		slog.Warn("cursorSession: close timed out, abandoning wg.Wait")
	}
	return nil
}

func truncateStr(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	return string([]rune(s)[:maxRunes]) + "..."
}
