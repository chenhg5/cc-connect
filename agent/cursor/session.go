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
	cmd       string   // CLI binary name
	extraArgs []string // extra args from cmd, prepended before agent args
	workDir   string
	model     string
	mode      string
	extraEnv  []string
	events    chan core.Event
	chatID    atomic.Value // stores string — Cursor chat/session ID
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	alive     atomic.Bool

	thinkingBuf strings.Builder // accumulate thinking deltas

	// Permission handling: each Send() creates a new process whose stdin is used
	// to respond to interaction_query permission requests.
	stdinMu sync.Mutex
	stdin   io.WriteCloser // current process stdin; nil when no process is running

	pendingMu sync.Mutex
	pending   *pendingInteractionQuery // most recent unresolved interaction_query/request

	// turnMu guards turnRunning, the WaitGroup Add, and events close so Send
	// cannot race Close's Wait (Add after Wait) or start a second process.
	turnMu       sync.Mutex
	turnRunning  bool
	eventsClosed bool

	// terminal is set when this turn has emitted EventResult or EventError.
	terminal atomic.Bool

	// deferTerminal is set by readLoop so a result is published only after
	// the turn is released. Direct handleResult calls (unit tests) emit immediately.
	deferTerminal atomic.Bool
	resultMu      sync.Mutex
	stashedResult *core.Event

	closeOnce  sync.Once
	turnGen    uint64
	pendingGen uint64
	wgDoneGen  uint64

	modelDisplay atomic.Value // string from system/init "model"

	toolMu   sync.Mutex
	toolSeen map[string]uint8 // bits: toolStarted, toolCompleted

	textMu          sync.Mutex
	streamedPartial strings.Builder // timestamped assistant deltas this turn
}

// pendingInteractionQuery holds the info needed to write a response back to Cursor.
type pendingInteractionQuery struct {
	id        int    // query["id"] value from the original request
	queryType string // e.g. "webFetchRequestQuery", "shellRequestQuery"
}

func newCursorSession(ctx context.Context, cmd string, extraArgs []string, workDir, model, mode, resumeID string, extraEnv []string) (*cursorSession, error) {
	sessionCtx, cancel := context.WithCancel(ctx)

	cs := &cursorSession{
		cmd:       cmd,
		extraArgs: extraArgs,
		workDir:   workDir,
		model:     model,
		mode:      mode,
		extraEnv:  extraEnv,
		events:    make(chan core.Event, 64),
		ctx:       sessionCtx,
		cancel:    cancel,
	}
	cs.alive.Store(true)

	if resumeID != "" && resumeID != core.ContinueSession {
		cs.chatID.Store(resumeID)
	}

	return cs, nil
}

func (cs *cursorSession) Send(prompt string, messageID string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if len(images) > 0 {
		// The Cursor Agent CLI has no image flag, but it can open image files
		// from disk and see their content. So route images through the same
		// on-disk channel as files instead of dropping them.
		imgFiles := make([]core.FileAttachment, 0, len(images))
		for i, im := range images {
			name := im.FileName
			if name == "" {
				// Feishu image messages carry no filename — synthesise one so
				// the file lands with a correct extension.
				name = fmt.Sprintf("img_%d_%d%s", time.Now().UnixMilli(), i, core.ExtFromMime(im.MimeType))
			}
			imgFiles = append(imgFiles, core.FileAttachment{
				MimeType: im.MimeType,
				Data:     im.Data,
				FileName: name,
			})
		}
		imagePaths := core.SaveFilesToDisk(cs.workDir, messageID, imgFiles)
		prompt = core.AppendImageRefs(prompt, imagePaths)
	}
	if len(files) > 0 {
		filePaths := core.SaveFilesToDisk(cs.workDir, messageID, files)
		prompt = core.AppendFileRefs(prompt, filePaths)
	}
	chatID := cs.CurrentSessionID()
	isResume := chatID != ""

	args := append(append([]string{}, cs.extraArgs...),
		"--print",
		"--output-format", "stream-json",
		"--stream-partial-output",
	)

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

	cs.turnMu.Lock()
	if !cs.alive.Load() {
		cs.turnMu.Unlock()
		return fmt.Errorf("session is closed")
	}
	if cs.turnRunning {
		cs.turnMu.Unlock()
		return fmt.Errorf("cursorSession: turn already in progress")
	}
	cs.turnGen++
	gen := cs.turnGen
	cs.turnRunning = true
	cs.terminal.Store(false)
	cs.wg.Add(1)
	cs.turnMu.Unlock()

	cs.resetTurnState()

	cmd := exec.Command(cs.cmd, args...)
	prepareCmdForKill(cmd)
	cmd.Dir = cs.workDir
	env := os.Environ()
	if len(cs.extraEnv) > 0 {
		env = core.MergeEnv(env, cs.extraEnv)
	}
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cs.finishTurn()
		return fmt.Errorf("cursorSession: stdout pipe: %w", err)
	}

	// Set up a stdin pipe so we can write interaction_query responses back to Cursor.
	// Without a connected stdin, Cursor gets EOF and auto-rejects all permission requests.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cs.finishTurn()
		return fmt.Errorf("cursorSession: stdin pipe: %w", err)
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		cs.finishTurn()
		return fmt.Errorf("cursorSession: start: %w", err)
	}

	cs.stdinMu.Lock()
	cs.stdin = stdin
	cs.stdinMu.Unlock()

	procDone := make(chan struct{})
	// Close waits for the process-tree kill as well as readLoop. Otherwise the
	// root can exit and release the turn before detached tool children are killed.
	cs.wg.Add(1)
	go func() {
		defer cs.wg.Done()
		select {
		case <-cs.ctx.Done():
			_ = forceKillCmd(cmd)
		case <-procDone:
		}
	}()

	go cs.readLoop(cmd, stdout, &stderrBuf, stdin, gen, procDone)

	return nil
}

func (cs *cursorSession) resetTurnState() {
	cs.thinkingBuf.Reset()
	cs.resultMu.Lock()
	cs.stashedResult = nil
	cs.resultMu.Unlock()
	cs.toolMu.Lock()
	cs.toolSeen = nil
	cs.toolMu.Unlock()
	cs.textMu.Lock()
	cs.streamedPartial.Reset()
	cs.textMu.Unlock()
	cs.clearPending()
}

// releaseTurn drops this generation's process state. A newer Send may already
// own stdin and turnRunning; those must not be cleared by the old process.
func (cs *cursorSession) releaseTurn(gen uint64, stdin io.WriteCloser) {
	if stdin != nil {
		cs.stdinMu.Lock()
		if cs.stdin == stdin {
			cs.stdin = nil
		}
		cs.stdinMu.Unlock()
		_ = stdin.Close()
	}
	cs.pendingMu.Lock()
	if cs.pendingGen == gen {
		cs.pending = nil
	}
	cs.pendingMu.Unlock()
	cs.turnMu.Lock()
	if cs.turnGen == gen && cs.turnRunning {
		cs.turnRunning = false
	}
	cs.turnMu.Unlock()
}

// finishWait balances Send's wg.Add. It runs after the terminal event is
// published so Close cannot close the events channel mid-send.
func (cs *cursorSession) finishWait(gen uint64) {
	cs.turnMu.Lock()
	defer cs.turnMu.Unlock()
	if cs.wgDoneGen == gen {
		return
	}
	cs.wgDoneGen = gen
	cs.wg.Done()
}

// finishTurn is the pre-start failure path (no generation yet).
func (cs *cursorSession) finishTurn() {
	cs.turnMu.Lock()
	gen := cs.turnGen
	cs.turnMu.Unlock()
	cs.releaseTurn(gen, nil)
	cs.finishWait(gen)
}

func (cs *cursorSession) readLoop(cmd *exec.Cmd, stdout io.ReadCloser, stderrBuf *bytes.Buffer, stdin io.WriteCloser, gen uint64, procDone chan struct{}) {
	defer close(procDone)
	cs.deferTerminal.Store(true)
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		cs.releaseTurn(gen, stdin)
	}
	defer cs.finishWait(gen)
	defer release()

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
		if cs.terminal.Load() {
			// Stop reading so a delayed process exit cannot hold the next Send.
			break
		}
	}

	scanErr := scanner.Err()
	if scanErr != nil {
		slog.Error("cursorSession: scanner error", "error", scanErr)
		// Unblock a child stuck writing a too-long line before Wait.
		_ = stdout.Close()
		_ = forceKillCmd(cmd)
	}

	// Drop the stdout pipe before Wait so a child blocked on write, or a
	// scanner that already stopped, cannot stall reaping.
	_ = stdout.Close()

	cs.resultMu.Lock()
	stashed := cs.stashedResult
	cs.stashedResult = nil
	cs.resultMu.Unlock()
	if stashed != nil {
		// Publish only after this turn no longer owns turnRunning/stdin,
		// so the engine can Send the next turn while this process exits.
		// Close stdout so a descendant still holding the pipe cannot stall Wait.
		_ = stdout.Close()
		release()
		cs.emit(*stashed)
		if err := cmd.Wait(); err != nil {
			stderrMsg := strings.TrimSpace(stderrBuf.String())
			slog.Warn("cursorSession: process exited after terminal event", "error", err, "stderr", truncateStr(stderrMsg, 200))
		}
		return
	}

	exitErr := cmd.Wait()
	release()
	if cs.terminal.Load() {
		return
	}
	if scanErr != nil && cs.ctx.Err() == nil {
		cs.emitTerminal(core.Event{Type: core.EventError, Error: fmt.Errorf("read stdout: %w", scanErr)})
		return
	}
	if exitErr != nil {
		stderrMsg := strings.TrimSpace(stderrBuf.String())
		if stderrMsg == "" {
			if cs.ctx.Err() != nil {
				stderrMsg = "cursor session cancelled"
			} else {
				stderrMsg = exitErr.Error()
			}
		}
		slog.Error("cursorSession: process failed with no result", "error", exitErr, "stderr", truncateStr(stderrMsg, 200))
		cs.emitTerminal(core.Event{Type: core.EventError, Error: fmt.Errorf("%s", stderrMsg)})
		return
	}
	if cs.ctx.Err() != nil {
		cs.emitTerminal(core.Event{Type: core.EventError, Error: fmt.Errorf("cursor session cancelled")})
		return
	}
	slog.Warn("cursorSession: process exited with no result event")
	cs.emitTerminal(core.Event{Type: core.EventError, Error: fmt.Errorf("cursor session ended without a result")})
}

func (cs *cursorSession) emit(evt core.Event) {
	cs.turnMu.Lock()
	closed := cs.eventsClosed
	cs.turnMu.Unlock()
	if closed {
		return
	}
	select {
	case cs.events <- evt:
	case <-cs.ctx.Done():
	}
}

func (cs *cursorSession) emitTerminal(evt core.Event) {
	if !cs.terminal.CompareAndSwap(false, true) {
		return
	}
	cs.emit(evt)
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
	subtype, _ := raw["subtype"].(string)
	if model := cursorModelDisplay(raw); model != "" && (subtype == "" || subtype == "init") {
		cs.modelDisplay.Store(model)
		slog.Debug("cursorSession: model", "model", model)
	}
	if sid, ok := raw["session_id"].(string); ok && sid != "" {
		cs.chatID.Store(sid)
		slog.Debug("cursorSession: session init", "session_id", sid)
		cs.emit(core.Event{Type: core.EventText, SessionID: sid, Content: ""})
	}
}

func cursorModelDisplay(raw map[string]any) string {
	switch m := raw["model"].(type) {
	case string:
		return strings.TrimSpace(m)
	case map[string]any:
		if s, ok := m["display_name"].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
		if s, ok := m["name"].(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func (cs *cursorSession) GetModelDisplayName() string {
	v, _ := cs.modelDisplay.Load().(string)
	return v
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
			cs.emit(core.Event{Type: core.EventThinking, Content: text})
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
	var blocks []string
	for _, contentItem := range contentArr {
		item, ok := contentItem.(map[string]any)
		if !ok {
			continue
		}
		contentType, _ := item["type"].(string)
		if contentType != "text" {
			continue
		}
		text, _ := item["text"].(string)
		if text == "" {
			continue
		}
		blocks = append(blocks, text)
	}
	if len(blocks) == 0 {
		return
	}
	// Real --stream-partial-output deltas have timestamp_ms and no model_call_id.
	// A completed assistant snapshot may still carry timestamp_ms when it also
	// has model_call_id (intermediate, before a tool) or neither field (final).
	_, hasTS := raw["timestamp_ms"]
	callID, _ := raw["model_call_id"].(string)
	if hasTS && callID == "" {
		for _, text := range blocks {
			cs.textMu.Lock()
			cs.streamedPartial.WriteString(text)
			cs.textMu.Unlock()
			cs.emit(core.Event{Type: core.EventText, Content: text})
		}
		return
	}
	joined := strings.Join(blocks, "")
	cs.textMu.Lock()
	prev := cs.streamedPartial.String()
	cs.streamedPartial.Reset()
	cs.textMu.Unlock()
	if prev != "" && joined == prev {
		return
	}
	if prev != "" && strings.HasPrefix(joined, prev) {
		suffix := strings.TrimPrefix(joined, prev)
		if suffix != "" {
			cs.emit(core.Event{Type: core.EventText, Content: suffix})
		}
		return
	}
	for _, text := range blocks {
		cs.emit(core.Event{Type: core.EventText, Content: text})
	}
}

func (cs *cursorSession) handleToolCall(raw map[string]any) {
	subtype, _ := raw["subtype"].(string)
	tc, _ := raw["tool_call"].(map[string]any)
	if tc == nil {
		return
	}

	key := toolCallKey(raw, tc)
	name, input := extractToolInfo(tc)

	if subtype == "started" {
		if name == "" || !cs.markTool(key, 1) {
			return
		}
		cs.emit(core.Event{Type: core.EventToolUse, ToolName: name, ToolInput: input})
		return
	}
	if subtype == "completed" {
		if !cs.markTool(key, 2) {
			return
		}
		content, status, success, exitCode := extractToolResult(tc)
		if name == "" {
			name, _ = extractToolInfo(tc)
		}
		slog.Debug("cursorSession: tool completed", "tool", name)
		cs.emit(core.Event{
			Type:         core.EventToolResult,
			ToolName:     name,
			ToolResult:   truncateStr(content, 2000),
			Content:      truncateStr(content, 2000),
			ToolStatus:   status,
			ToolExitCode: exitCode,
			ToolSuccess:  success,
		})
	}
}

// markTool records a tool phase. phase 1 is start, 2 is completion.
// Repeated events for the same stable id are dropped. An empty key means the
// call has no id and must not be collapsed with another identical call.
func (cs *cursorSession) markTool(key string, phase uint8) bool {
	if key == "" {
		return true
	}
	cs.toolMu.Lock()
	defer cs.toolMu.Unlock()
	if cs.toolSeen == nil {
		cs.toolSeen = make(map[string]uint8)
	}
	prev := cs.toolSeen[key]
	if prev >= phase {
		return false
	}
	cs.toolSeen[key] = phase
	return true
}

func toolCallKey(raw, tc map[string]any) string {
	if id := firstStringID(raw, "call_id", "tool_call_id", "toolCallId"); id != "" {
		return id
	}
	for _, v := range tc {
		child, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if id := firstStringID(child, "toolCallId", "tool_call_id", "call_id"); id != "" {
			return id
		}
	}
	return ""
}

func firstStringID(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if id, ok := m[k].(string); ok && id != "" {
			return id
		}
	}
	return ""
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
	cs.turnMu.Lock()
	gen := cs.turnGen
	cs.turnMu.Unlock()
	cs.pendingMu.Lock()
	cs.pending = &pendingInteractionQuery{id: queryID, queryType: queryType}
	cs.pendingGen = gen
	cs.pendingMu.Unlock()

	// Build a stable requestID that the engine passes back to RespondPermission.
	requestID := fmt.Sprintf("%s:%d", queryType, queryID)

	slog.Info("cursorSession: permission request", "request_id", requestID, "tool", toolName)
	cs.emit(core.Event{
		Type:      core.EventPermissionRequest,
		RequestID: requestID,
		ToolName:  toolName,
		ToolInput: inputStr,
	})
}

func (cs *cursorSession) clearPending() {
	cs.pendingMu.Lock()
	cs.pending = nil
	cs.pendingMu.Unlock()
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

func extractToolResult(tc map[string]any) (content, status string, success *bool, exitCode *int) {
	status = "completed"
	for _, key := range []string{"shellToolCall", "readToolCall", "editToolCall", "writeToolCall", "listToolCall", "searchToolCall", "grepToolCall", "globToolCall", "webFetchToolCall"} {
		call, ok := tc[key].(map[string]any)
		if !ok {
			continue
		}
		return toolResultFromCall(call)
	}
	if result, ok := tc["result"]; ok {
		return toolResultValue(result)
	}
	return "", status, nil, nil
}

func toolResultFromCall(call map[string]any) (content, status string, success *bool, exitCode *int) {
	if result, ok := call["result"]; ok {
		return toolResultValue(result)
	}
	return "", "completed", nil, nil
}

func toolResultValue(result any) (content, status string, success *bool, exitCode *int) {
	status = "completed"
	switch r := result.(type) {
	case string:
		ok := true
		return r, status, &ok, nil
	case map[string]any:
		if successMap, ok := r["success"].(map[string]any); ok {
			s := true
			return toolResultText(successMap), "completed", &s, intFromAny(successMap["exitCode"])
		}
		for _, failKey := range []string{"error", "failure", "rejected"} {
			fail, ok := r[failKey].(map[string]any)
			if !ok {
				continue
			}
			s := false
			st := "failed"
			if failKey == "rejected" {
				st = "rejected"
			}
			return toolResultText(fail), st, &s, intFromAny(fail["exitCode"])
		}
		s := true
		return toolResultText(r), status, &s, intFromAny(r["exitCode"])
	default:
		return "", status, nil, nil
	}
}

func toolResultText(m map[string]any) string {
	var parts []string
	for _, k := range []string{"stdout", "output", "content", "text", "diffString", "message", "error", "reason", "stderr"} {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			parts = append(parts, s)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func intFromAny(v any) *int {
	switch n := v.(type) {
	case float64:
		i := int(n)
		return &i
	case int:
		i := n
		return &i
	default:
		return nil
	}
}

func (cs *cursorSession) handleResult(raw map[string]any) {
	var content string
	if result, ok := raw["result"].(string); ok {
		content = result
	}
	if sid, ok := raw["session_id"].(string); ok && sid != "" {
		cs.chatID.Store(sid)
	}

	isError, _ := raw["is_error"].(bool)
	if !isError {
		if sub, _ := raw["subtype"].(string); strings.EqualFold(sub, "error") {
			isError = true
		}
	}
	if isError {
		msg := strings.TrimSpace(content)
		if msg == "" {
			msg = "cursor result error"
		}
		cs.noteTerminal(core.Event{Type: core.EventError, Error: fmt.Errorf("%s", msg), Content: msg, SessionID: cs.CurrentSessionID()})
		return
	}

	// Result text is the final answer. Assistant snapshots already emitted
	// EventText; this does not emit another text event.
	// Cursor emits a `usage` block on the result event with camelCase keys
	// (inputTokens / outputTokens / cacheReadTokens / cacheWriteTokens).
	// Without this, the engine's turn-complete log always reports 0 tokens
	// and the reply footer is suppressed by #701's workdir-only guard. See
	// issue #1810.
	var inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens int
	if usage, ok := raw["usage"].(map[string]any); ok {
		inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens = parseCursorUsage(usage)
	}
	cs.noteTerminal(core.Event{
		Type:                     core.EventResult,
		Content:                  content,
		SessionID:                cs.CurrentSessionID(),
		Done:                     true,
		InputTokens:              inputTokens,
		OutputTokens:             outputTokens,
		CacheReadInputTokens:     cacheReadTokens,
		CacheCreationInputTokens: cacheWriteTokens,
	})
}

// noteTerminal records the single terminal event. readLoop publishes it after
// releasing the turn; other callers publish immediately.
func (cs *cursorSession) noteTerminal(evt core.Event) {
	if !cs.terminal.CompareAndSwap(false, true) {
		return
	}
	if cs.deferTerminal.Load() {
		cs.resultMu.Lock()
		if cs.stashedResult == nil {
			copied := evt
			cs.stashedResult = &copied
		}
		cs.resultMu.Unlock()
		return
	}
	cs.emit(evt)
}

// parseCursorUsage extracts token counts from a Cursor result event's `usage`
// object. The Cursor Agent CLI emits camelCase keys (inputTokens,
// outputTokens, cacheReadTokens, cacheWriteTokens) — different from
// Claude Code's snake_case — so this helper mirrors agent/claudecode's
// parseClaudeUsage but uses the right field names. Returns 0 for any
// field that is missing or not numeric, so callers can safely emit an
// Event with partial usage (e.g. when the CLI downgrades the schema).
func parseCursorUsage(usage map[string]any) (input, output, cacheRead, cacheWrite int) {
	if v, ok := usage["inputTokens"].(float64); ok {
		input = int(v)
	}
	if v, ok := usage["outputTokens"].(float64); ok {
		output = int(v)
	}
	if v, ok := usage["cacheReadTokens"].(float64); ok {
		cacheRead = int(v)
	}
	if v, ok := usage["cacheWriteTokens"].(float64); ok {
		cacheWrite = int(v)
	}
	return
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
	cs.closeOnce.Do(func() {
		cs.turnMu.Lock()
		cs.alive.Store(false)
		cs.cancel()
		cs.turnMu.Unlock()
		cs.clearPending()
		done := make(chan struct{})
		go func() {
			cs.wg.Wait()
			close(done)
		}()
		select {
		case <-done:
			cs.turnMu.Lock()
			if !cs.eventsClosed {
				cs.eventsClosed = true
				close(cs.events)
			}
			cs.turnMu.Unlock()
		case <-time.After(8 * time.Second):
			slog.Warn("cursorSession: close timed out, abandoning wg.Wait")
		}
	})
	return nil
}

func truncateStr(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	return string([]rune(s)[:maxRunes]) + "..."
}
