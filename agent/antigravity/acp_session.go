package antigravity

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// agy 1.5.13 fixed ACP's session working directory and attached file context.
// Earlier CLIs and wrappers without a version surface retain the print adapter.
func supportsAgyACP(ctx context.Context, command string, extraArgs []string, workDir string, env []string) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, command, append(append([]string{}, extraArgs...), "--version")...)
	cmd.Dir = workDir
	cmd.Env = core.MergeEnv(os.Environ(), env)
	cmd.WaitDelay = time.Second
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	var major, minor, patch int
	version := strings.TrimSpace(string(output))
	if _, err := fmt.Sscanf(version, "%d.%d.%d", &major, &minor, &patch); err != nil {
		return false
	}
	return major > 1 || major == 1 && (minor > 5 || minor == 5 && patch >= 13)
}

type agyACPReply struct {
	result json.RawMessage
	err    error
}

type agyACPPrompt struct {
	// Resolve the send only from this prompt's own reply, never from a predecessor.
	handover chan error
}

type agyACPRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *agyACPRPCError) Error() string {
	return fmt.Sprintf("%d: %s", e.Code, e.Message)
}

const (
	agyACPDefaultTurnTimeout = 15 * time.Minute
	agyACPSettlementTimeout  = 45 * time.Second
)

type agyACPPermission struct {
	id      json.RawMessage
	options []struct {
		ID   string `json:"optionId"`
		Kind string `json:"kind"`
	}
}

type agyACPSession struct {
	*antigravitySession
	effort                string
	ctxWindowOverride     int
	usageMu               sync.Mutex
	lastUsage             *core.ContextUsage
	stdin                 io.WriteCloser
	writeMu               sync.Mutex
	stateMu               sync.Mutex
	nextID                int64
	waiters               map[string]chan agyACPReply
	prompts               map[string]*agyACPPrompt
	permissions           map[string]agyACPPermission
	ready                 bool
	thought               strings.Builder
	text                  strings.Builder
	usage                 agyACPTokenUsage
	turnError             error
	turnTimer             *time.Timer
	turnSequence          uint64
	turnDeadline          time.Time
	usageIncomplete       bool
	contextPrompt         int64
	reportedContextWindow int
}

func newAgyACPSession(ctx context.Context, command string, extraArgs []string, workDir, model, effort, mode, resumeID string, env []string, timeout time.Duration, window int) (*agyACPSession, error) {
	absDir, err := filepath.Abs(workDir)
	if err != nil {
		return nil, err
	}
	s := &agyACPSession{
		antigravitySession: newAntigravitySessionState(ctx, command, extraArgs, absDir, model, mode, resumeID, env, timeout),
		effort:             effort, ctxWindowOverride: window, waiters: make(map[string]chan agyACPReply), prompts: make(map[string]*agyACPPrompt), permissions: make(map[string]agyACPPermission),
	}
	args := append(append([]string{}, extraArgs...), "acp")
	switch mode {
	case "yolo":
		args = append(args, "--dangerously-skip-permissions")
	case "plan":
		args = append(args, "--sandbox")
	}
	cmd := exec.CommandContext(s.ctx, command, args...)
	cmd.Dir = absDir
	cmd.Env = core.MergeEnv(os.Environ(), env)
	cmd.WaitDelay = time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		s.cancel()
		return nil, err
	}
	s.stdin = stdin
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		s.cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		s.cancel()
		return nil, fmt.Errorf("antigravity ACP start: %w", err)
	}
	s.wg.Add(1)
	go s.readACP(cmd, stdout)
	if err := s.initializeACP(resumeID); err != nil {
		_ = s.Close()
		return nil, err
	}
	s.stateMu.Lock()
	s.ready = true
	s.stateMu.Unlock()
	return s, nil
}

func (s *agyACPSession) initializeACP(resumeID string) error {
	if _, err := s.callACP("initialize", map[string]any{
		"protocolVersion": 1, "clientCapabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "cc-connect", "version": "1"},
	}); err != nil {
		return err
	}
	method := "session/new"
	params := map[string]any{"cwd": s.workDir, "mcpServers": []any{}}
	if resumeID != "" && resumeID != core.ContinueSession {
		method = "session/resume"
		params["sessionId"] = resumeID
	}
	reply, err := s.callACP(method, params)
	if err != nil {
		return err
	}
	var created struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(reply, &created); err != nil || created.SessionID == "" {
		return fmt.Errorf("antigravity ACP %s returned no session id", method)
	}
	s.chatID.Store(created.SessionID)
	if s.model != "" {
		if err := s.setACPOption("session/set_model", map[string]any{"sessionId": created.SessionID, "modelId": s.model}, true); err != nil {
			return err
		}
	}
	if s.effort != "" {
		if err := s.setACPOption("session/set_config_option", map[string]any{"sessionId": created.SessionID, "configId": "thought_level", "value": s.effort}, true); err != nil {
			return err
		}
	}
	if s.mode != "yolo" {
		if err := s.setACPOption("session/set_mode", map[string]any{"sessionId": created.SessionID, "modeId": s.mode}, s.mode == "" || s.mode == "default"); err != nil {
			return err
		}
	}
	return nil
}

func (s *agyACPSession) setACPOption(method string, params map[string]any, optional bool) error {
	_, err := s.callACP(method, params)
	if ctxErr := s.ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	var rpcErr *agyACPRPCError
	if optional && errors.As(err, &rpcErr) &&
		(rpcErr.Code == -32601 || rpcErr.Code == -32603 &&
			strings.EqualFold(strings.TrimSpace(rpcErr.Message), "method not supported by this agent")) {
		slog.Warn("antigravity ACP option unsupported, using agent default", "method", method)
		return nil
	}
	return err
}

// ACP agents may echo a numeric request ID as a JSON string.
func agyACPResponseID(raw json.RawMessage) string {
	value := string(raw)
	if len(raw) > 0 && raw[0] == '"' {
		if json.Unmarshal(raw, &value) != nil {
			return ""
		}
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

func (s *agyACPSession) writeACP(value any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return json.NewEncoder(s.stdin).Encode(value)
}

func (s *agyACPSession) requestACP(method string, params any) (string, map[string]any) {
	s.nextID++
	id := strconv.FormatInt(s.nextID, 10)
	return id, map[string]any{"jsonrpc": "2.0", "id": s.nextID, "method": method, "params": params}
}

func (s *agyACPSession) callACP(method string, params any) (json.RawMessage, error) {
	s.stateMu.Lock()
	id, request := s.requestACP(method, params)
	ch := make(chan agyACPReply, 1)
	s.waiters[id] = ch
	err := s.writeACP(request)
	s.stateMu.Unlock()
	defer func() { s.stateMu.Lock(); delete(s.waiters, id); s.stateMu.Unlock() }()
	if err != nil {
		return nil, err
	}
	timer := time.NewTimer(45 * time.Second)
	defer timer.Stop()
	select {
	case reply := <-ch:
		if reply.err != nil {
			return nil, fmt.Errorf("antigravity ACP %s: %w", method, reply.err)
		}
		return reply.result, nil
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	case <-timer.C:
		return nil, fmt.Errorf("antigravity ACP %s timed out", method)
	}
}

func (s *agyACPSession) Send(prompt, messageID string, images []core.ImageAttachment, files []core.FileAttachment) error {
	return s.sendACPPrompt(prompt, messageID, images, files, false)
}

func (s *agyACPSession) SendMidTurn(prompt, messageID string, images []core.ImageAttachment, files []core.FileAttachment) error {
	return s.sendACPPrompt(prompt, messageID, images, files, true)
}

func (s *agyACPSession) sendACPPrompt(prompt, messageID string, images []core.ImageAttachment, files []core.FileAttachment, midTurn bool) error {
	s.stateMu.Lock()
	if !s.Alive() || !s.ready {
		s.stateMu.Unlock()
		return fmt.Errorf("antigravity ACP session is closed")
	}
	if midTurn && len(s.prompts) == 0 {
		s.stateMu.Unlock()
		return fmt.Errorf("antigravity ACP active turn changed")
	}
	// /ps uses the normal Send entry point to supplement a live turn.
	midTurn = len(s.prompts) > 0
	if midTurn && len(s.permissions) > 0 {
		s.stateMu.Unlock()
		return fmt.Errorf("antigravity ACP is waiting for a permission or question answer")
	}
	paths := core.SaveFilesToDisk(s.workDir, messageID, files)
	prompt = core.AppendFileRefs(prompt, paths)
	blocks := []any{map[string]any{"type": "text", "text": prompt}}
	for _, img := range images {
		mime := img.MimeType
		if mime == "" {
			mime = "image/png"
		}
		blocks = append(blocks, map[string]any{"type": "image", "mimeType": mime, "data": base64.StdEncoding.EncodeToString(img.Data)})
	}
	id, request := s.requestACP("session/prompt", map[string]any{"sessionId": s.CurrentSessionID(), "prompt": blocks})
	pending := &agyACPPrompt{}
	if midTurn {
		pending.handover = make(chan error, 1)
	}
	handover := pending.handover
	s.prompts[id] = pending
	if err := s.writeACP(request); err != nil {
		delete(s.prompts, id)
		s.stateMu.Unlock()
		return err
	}
	if !midTurn {
		s.turnSequence++
		timeout := s.timeout
		if timeout <= 0 {
			timeout = agyACPDefaultTurnTimeout
		}
		s.armACPTurnTimeout(timeout)
	}
	s.stateMu.Unlock()
	if handover != nil {
		select {
		case err := <-handover:
			return err
		case <-s.ctx.Done():
			// Timeout may have delivered the definitive outcome before
			// stopping the process; prefer that result over context cancellation.
			select {
			case err := <-handover:
				return err
			default:
				return s.ctx.Err()
			}
		}
	}
	return nil
}

// Called with stateMu held. Tightening a deadline never extends the turn.
func (s *agyACPSession) armACPTurnTimeout(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	if s.turnTimer != nil {
		if !deadline.Before(s.turnDeadline) {
			return
		}
		s.turnTimer.Stop()
	}
	s.turnDeadline = deadline
	sequence := s.turnSequence
	s.turnTimer = time.AfterFunc(time.Until(deadline), func() {
		s.stateMu.Lock()
		defer s.stateMu.Unlock()
		if sequence != s.turnSequence || s.turnDeadline != deadline || len(s.prompts) == 0 || s.ctx.Err() != nil {
			return
		}
		err := errors.New("antigravity ACP prompt timed out before a terminal response")
		// Fence the process: late responses or updates must not contaminate
		// the next turn. Flush buffered text and counters before stopping it.
		s.alive.Store(false)
		for _, pending := range s.prompts {
			if pending.handover != nil {
				pending.handover <- err
			}
		}
		clear(s.prompts)
		s.turnError = err
		s.usageIncomplete = true
		s.finishACPTurn()
		s.cancel()
	})
}

func (s *agyACPSession) emitACP(event core.Event) {
	event.SessionID = s.CurrentSessionID()
	select {
	case s.events <- event:
	case <-s.ctx.Done():
	}
}

func (s *agyACPSession) flushACPThought() {
	if s.thought.Len() == 0 {
		return
	}
	if content := strings.TrimRight(s.thought.String(), "\r\n"); strings.TrimSpace(content) != "" {
		s.emitACP(core.Event{Type: core.EventThinking, Content: content})
	}
	s.thought.Reset()
}

func (s *agyACPSession) flushACPText(kind core.EventType) {
	if s.text.Len() == 0 {
		return
	}
	s.emitACP(core.Event{Type: kind, Content: s.text.String()})
	s.text.Reset()
}

func (s *agyACPSession) acpContextWindow() int {
	if s.ctxWindowOverride > 0 {
		return s.ctxWindowOverride
	}
	if s.reportedContextWindow > 0 {
		return s.reportedContextWindow
	}
	return antigravityContextWindow(s.model, 0)
}

func (s *agyACPSession) readACP(cmd *exec.Cmd, stdout io.ReadCloser) {
	defer s.wg.Done()
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)
	for scanner.Scan() {
		s.dispatchACP(scanner.Bytes())
	}
	err := cmd.Wait()
	if err == nil {
		err = io.EOF
	}
	s.stateMu.Lock()
	s.alive.Store(false)
	for _, ch := range s.waiters {
		ch <- agyACPReply{err: err}
	}
	for _, pending := range s.prompts {
		if pending.handover != nil {
			pending.handover <- err
		}
	}
	if len(s.prompts) > 0 && s.ctx.Err() == nil {
		s.turnError = fmt.Errorf("antigravity ACP stream closed before turn completion: %w", err)
		s.usageIncomplete = true
		clear(s.prompts)
		s.finishACPTurn()
	}
	s.stateMu.Unlock()
}

func (s *agyACPSession) dispatchACP(line []byte) {
	var message struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		Result json.RawMessage `json:"result"`
		Error  *agyACPRPCError `json:"error"`
	}
	if json.Unmarshal(line, &message) != nil {
		return
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.ctx.Err() != nil {
		return
	}
	if message.Method != "" {
		if message.Method == "session/update" && s.ready && len(s.prompts) > 0 {
			s.updateACP(message.Params)
		}
		if message.Method == "session/request_permission" {
			s.permissionACP(message.ID, message.Params)
		}
		if message.Method != "session/request_permission" && len(message.ID) > 0 && string(message.ID) != "null" {
			_ = s.writeACP(map[string]any{"jsonrpc": "2.0", "id": message.ID, "error": map[string]any{"code": -32601, "message": "Method not supported"}})
		}
		return
	}
	id := agyACPResponseID(message.ID)
	if id == "" {
		return
	}
	var rpcErr error
	if message.Error != nil {
		rpcErr = message.Error
	}
	if ch, ok := s.waiters[id]; ok {
		delete(s.waiters, id)
		ch <- agyACPReply{result: message.Result, err: rpcErr}
		return
	}
	pending, ok := s.prompts[id]
	if !ok {
		return
	}
	delete(s.prompts, id)
	var response struct {
		StopReason string `json:"stopReason"`
		Usage      *struct {
			Input      int `json:"inputTokens"`
			Output     int `json:"outputTokens"`
			Cache      int `json:"cachedReadTokens"`
			CacheWrite int `json:"cachedWriteTokens"`
			Thought    int `json:"thoughtTokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(message.Result, &response); rpcErr == nil && (err != nil || response.StopReason == "") {
		rpcErr = fmt.Errorf("antigravity ACP prompt returned no valid stopReason")
	}
	if pending.handover != nil {
		pending.handover <- rpcErr
		if rpcErr != nil {
			// No prior completion can acknowledge this send. A rejected supplement
			// never replaces the original turn's result, regardless of reply order.
			if len(s.prompts) == 0 {
				s.finishACPTurn()
			}
			return
		}
	}
	if rpcErr != nil {
		s.turnError = rpcErr
	}
	if response.Usage != nil {
		u := response.Usage
		s.usage.Add(agyACPTokenUsage{InputTokens: u.Input, OutputTokens: u.Output, CacheReadInputTokens: u.Cache, CacheCreationInputTokens: u.CacheWrite, ReasoningOutputTokens: u.Thought})
		promptID, _ := strconv.ParseInt(id, 10, 64)
		if promptID >= s.contextPrompt {
			s.contextPrompt = promptID
			s.usageMu.Lock()
			s.lastUsage = &core.ContextUsage{UsedTokens: u.Input + u.Cache, TotalTokens: u.Input + u.Cache + u.Output, InputTokens: u.Input, CachedInputTokens: u.Cache, OutputTokens: u.Output, ReasoningOutputTokens: u.Thought, ContextWindow: s.acpContextWindow()}
			s.usageMu.Unlock()
		}
	} else {
		s.usageIncomplete = true
	}
	// A superseded prompt answers end_turn immediately. Its reply must not
	// finish the engine turn, cancel its permissions, or drain queued input.
	if len(s.prompts) == 0 {
		s.finishACPTurn()
	} else {
		s.armACPTurnTimeout(agyACPSettlementTimeout)
	}
}

func (s *agyACPSession) finishACPTurn() {
	if s.turnTimer != nil {
		s.turnTimer.Stop()
		s.turnTimer = nil
	}
	s.flushACPThought()
	s.flushACPText(core.EventText)
	if s.turnError != nil {
		s.emitACP(core.Event{Type: core.EventError, Error: s.turnError})
	}
	event := core.Event{Type: core.EventResult, Done: true, Metadata: map[string]any{"agy_usage_incomplete": s.usageIncomplete}}
	s.usage.Apply(&event)
	s.emitACP(event)
	s.usage = agyACPTokenUsage{}
	s.turnError = nil
	s.usageIncomplete = false
	clear(s.permissions)
}

type acpPlanEntry struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

func (s *agyACPSession) updateACP(params json.RawMessage) {
	var notification struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			Kind      string          `json:"sessionUpdate"`
			ID        string          `json:"toolCallId"`
			Name      string          `json:"name"`
			Title     string          `json:"title"`
			Status    string          `json:"status"`
			RawInput  map[string]any  `json:"rawInput"`
			RawOutput json.RawMessage `json:"rawOutput"`
			Content   json.RawMessage `json:"content"`
			Entries   []acpPlanEntry  `json:"entries"`
			Used      int             `json:"used"`
			Size      int             `json:"size"`
		} `json:"update"`
	}
	if json.Unmarshal(params, &notification) != nil || notification.SessionID != s.CurrentSessionID() {
		return
	}
	u := notification.Update
	switch u.Kind {
	case "agent_thought_chunk":
		if text, ok := extractACPTextBlock(u.Content); ok {
			s.thought.WriteString(text)
		}
	case "agent_message_chunk":
		s.flushACPThought()
		if text, ok := extractACPTextBlock(u.Content); ok {
			s.text.WriteString(text)
		}
	case "tool_call":
		s.flushACPThought()
		s.flushACPText(core.EventText)
		toolName := firstNonEmptyACP(u.Name, u.Title)
		if toolName == "invoke_subagent" || toolName == "manage_subagents" || toolName == "swarm" {
			s.usageIncomplete = true
		}
		s.emitACP(core.Event{
			Type:         core.EventToolUse,
			ToolName:     toolName,
			ToolInput:    formatACPToolInput(u.RawInput),
			ToolInputRaw: u.RawInput,
		})
	case "tool_call_update":
		if u.Status != "completed" && u.Status != "failed" {
			return
		}
		s.flushACPThought()
		// ACP rawOutput can be any JSON value, not just an object.
		var outputFields map[string]any
		_ = json.Unmarshal(u.RawOutput, &outputFields)
		exitCode := extractACPExitCode(outputFields)
		success := u.Status == "completed" && (exitCode == nil || *exitCode == 0)
		s.emitACP(core.Event{
			Type:         core.EventToolResult,
			ToolName:     firstNonEmptyACP(u.Name, u.Title),
			ToolStatus:   u.Status,
			ToolResult:   extractACPToolResult(u.Content, u.RawOutput, outputFields),
			ToolExitCode: exitCode,
			ToolSuccess:  &success,
		})
	case "plan":
		if formatted := formatACPPlan(u.Entries); formatted != "" {
			s.flushACPThought()
			s.emitACP(core.Event{Type: core.EventThinking, Content: formatted})
		}
	case "usage_update":
		if u.Size > 0 {
			s.reportedContextWindow = u.Size
		}
		if u.Used > 0 {
			ctxWin := s.acpContextWindow()
			s.usageMu.Lock()
			s.lastUsage = &core.ContextUsage{
				UsedTokens:    u.Used,
				TotalTokens:   u.Used,
				InputTokens:   u.Used,
				ContextWindow: ctxWin,
			}
			s.usageMu.Unlock()
		}
	}
}

func (s *agyACPSession) permissionACP(id json.RawMessage, params json.RawMessage) {
	var request struct {
		SessionID string `json:"sessionId"`
		Tool      struct {
			Name     string         `json:"name"`
			Title    string         `json:"title"`
			RawInput map[string]any `json:"rawInput"`
		} `json:"toolCall"`
		Options []struct {
			ID   string `json:"optionId"`
			Kind string `json:"kind"`
		} `json:"options"`
	}
	if json.Unmarshal(params, &request) != nil || request.SessionID != s.CurrentSessionID() {
		_ = s.writeACP(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}})
		return
	}
	s.flushACPThought()
	key := string(id)
	s.permissions[key] = agyACPPermission{id: id, options: request.Options}
	s.emitACP(core.Event{
		Type:         core.EventPermissionRequest,
		RequestID:    key,
		ToolName:     firstNonEmptyACP(request.Tool.Name, request.Tool.Title),
		ToolInput:    formatACPToolInput(request.Tool.RawInput),
		ToolInputRaw: request.Tool.RawInput,
	})
}

var acpToolInputKeys = []string{
	"CommandLine",
	"command",
	"Path",
	"path",
	"TargetFile",
	"file_path",
	"DirectoryPath",
	"SearchPath",
	"SearchDirectory",
	"AbsolutePath",
	"Query",
	"query",
	"Pattern",
	"Url",
	"url",
}

func firstNonEmptyACP(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func extractACPTextBlock(raw json.RawMessage) (string, bool) {
	var block struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &block) == nil && block.Type == "text" {
		return block.Text, true
	}
	return "", false
}

func formatACPToolInput(input map[string]any) string {
	if len(input) == 0 {
		return ""
	}
	for _, key := range acpToolInputKeys {
		if val, ok := input[key].(string); ok && strings.TrimSpace(val) != "" {
			return val
		}
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return ""
	}
	return string(raw)
}

func extractACPToolContentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	type contentItem struct {
		Type    string          `json:"type"`
		Text    string          `json:"text"`
		Content json.RawMessage `json:"content"`
	}
	extractItem := func(item contentItem) string {
		if item.Type == "text" && item.Text != "" {
			return item.Text
		}
		if item.Type == "content" && len(item.Content) > 0 {
			if text, ok := extractACPTextBlock(item.Content); ok {
				return text
			}
		}
		return ""
	}
	var items []contentItem
	if json.Unmarshal(raw, &items) == nil {
		var parts []string
		for _, item := range items {
			if text := extractItem(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	}
	var single contentItem
	if json.Unmarshal(raw, &single) == nil {
		if text := extractItem(single); text != "" {
			return text
		}
	}
	var plain string
	if json.Unmarshal(raw, &plain) == nil {
		return plain
	}
	return ""
}

func extractACPToolResult(content, rawOutput json.RawMessage, outputFields map[string]any) string {
	if text := extractACPToolContentText(content); text != "" {
		return text
	}
	if len(outputFields) == 0 {
		var plain string
		_ = json.Unmarshal(rawOutput, &plain)
		return plain
	}
	var parts []string
	for _, key := range []string{"output", "stdout", "stderr", "error", "message", "result"} {
		if val, ok := outputFields[key].(string); ok && val != "" {
			parts = append(parts, val)
		}
	}
	return strings.Join(parts, "\n")
}

func extractACPExitCode(rawOutput map[string]any) *int {
	for _, key := range []string{"exit_code", "exitCode"} {
		val, ok := rawOutput[key]
		if !ok || val == nil {
			continue
		}
		switch n := val.(type) {
		case float64:
			code := int(n)
			return &code
		case int:
			code := n
			return &code
		case int64:
			code := int(n)
			return &code
		}
	}
	return nil
}

func formatACPPlan(entries []acpPlanEntry) string {
	if len(entries) == 0 {
		return ""
	}
	var lines []string
	for _, entry := range entries {
		content := strings.TrimSpace(entry.Content)
		if content == "" {
			continue
		}
		status := strings.TrimSpace(entry.Status)
		if status != "" {
			lines = append(lines, "["+status+"] "+content)
		} else {
			lines = append(lines, content)
		}
	}
	return strings.Join(lines, "\n")
}

func (s *agyACPSession) RespondPermission(id string, result core.PermissionResult) error {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	request, ok := s.permissions[id]
	if !ok {
		return fmt.Errorf("antigravity ACP unknown permission %q", id)
	}
	kind := "reject_once"
	if result.Behavior == "allow" {
		kind = "allow_once"
	}
	outcome := map[string]any{"outcome": "cancelled"}
	for _, option := range request.options {
		if option.Kind == kind {
			outcome = map[string]any{"outcome": "selected", "optionId": option.ID}
			break
		}
	}
	if err := s.writeACP(map[string]any{"jsonrpc": "2.0", "id": request.id, "result": map[string]any{"outcome": outcome}}); err != nil {
		return err
	}
	delete(s.permissions, id)
	return nil
}

func (s *agyACPSession) CancelTurn() error {
	s.stateMu.Lock()
	if len(s.prompts) > 0 && s.ctx.Err() == nil {
		s.armACPTurnTimeout(agyACPSettlementTimeout)
	}
	s.stateMu.Unlock()
	return s.writeACP(map[string]any{"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]any{"sessionId": s.CurrentSessionID()}})
}

func (s *agyACPSession) Close() error {
	// Unblock a reader emitting into a full event buffer before taking its
	// state lock; the engine may have stopped consuming events already.
	s.alive.Store(false)
	s.cancel()
	s.stateMu.Lock()
	if s.turnTimer != nil {
		s.turnTimer.Stop()
	}
	s.stateMu.Unlock()
	_ = s.stdin.Close()
	return s.antigravitySession.Close()
}

// These are disjoint ACP per-prompt counters, not cumulative session totals.
type agyACPTokenUsage struct {
	InputTokens, OutputTokens, CacheReadInputTokens, CacheCreationInputTokens, ReasoningOutputTokens int
}

func (u *agyACPTokenUsage) Add(v agyACPTokenUsage) {
	u.InputTokens += v.InputTokens
	u.OutputTokens += v.OutputTokens
	u.CacheReadInputTokens += v.CacheReadInputTokens
	u.CacheCreationInputTokens += v.CacheCreationInputTokens
	u.ReasoningOutputTokens += v.ReasoningOutputTokens
}
func (u agyACPTokenUsage) Apply(e *core.Event) {
	e.InputTokens = u.InputTokens
	e.OutputTokens = u.OutputTokens
	e.CacheReadInputTokens = u.CacheReadInputTokens
	e.CacheCreationInputTokens = u.CacheCreationInputTokens
}
func (s *agyACPSession) GetContextUsage() *core.ContextUsage {
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	if s.lastUsage == nil {
		return nil
	}
	usage := *s.lastUsage
	return &usage
}
func (s *agyACPSession) GetUsageSource() string {
	if s.GetContextUsage() == nil {
		return ""
	}
	return "event"
}

func antigravityContextWindow(model string, override int) int {
	if override > 0 {
		return override
	}
	lower := strings.ToLower(strings.TrimSpace(model))
	if lower == "" {
		return 1_000_000
	}
	if strings.Contains(lower, "[2m]") {
		return 2_000_000
	}
	if strings.Contains(lower, "[1m]") {
		return 1_000_000
	}
	if strings.Contains(lower, "gemini-1.5-pro") {
		return 2_000_000
	}
	if strings.Contains(lower, "gemini-2.5-pro") || strings.Contains(lower, "gemini-2.5") {
		return 1_048_576
	}
	if strings.Contains(lower, "gemini") {
		return 1_000_000
	}
	if strings.Contains(lower, "claude") {
		return 200_000
	}
	if strings.Contains(lower, "o1") || strings.Contains(lower, "o3") {
		return 200_000
	}
	if strings.Contains(lower, "gpt-4") {
		return 128_000
	}
	return 1_000_000
}
