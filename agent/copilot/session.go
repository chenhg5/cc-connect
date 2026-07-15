package copilot

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// copilotSession manages a long-running Copilot CLI process using
// JSON-RPC 2.0 over Content-Length framed stdio.
type copilotSession struct {
	cmd       *exec.Cmd
	rpc       *rpcClient
	reader    *lspReader
	events    chan core.Event
	sessionID atomic.Value // stores string - Copilot session ID
	mode      string       // permission mode
	model     string
	provider  *copilotWireProviderConfig
	workDir   string
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	alive     atomic.Bool

	// autoApprove is set when mode == "bypassPermissions"
	autoApprove atomic.Bool

	// pendingPermissions maps Copilot requestId to JSON-RPC request ids
	// for permission.request server-to-client RPC calls. eventPermissions
	// tracks permission.requested session events, which are answered via
	// session.permissions.handlePendingPermissionRequest.
	pendingPermMu      sync.Mutex
	pendingPermissions map[string]json.RawMessage
	eventPermissions   map[string]struct{}

	// context usage tracking
	contextMu    sync.RWMutex
	contextUsage *core.ContextUsage
}

type copilotWireProviderConfig struct {
	Type        string            `json:"type,omitempty"`
	WireAPI     string            `json:"wireApi,omitempty"`
	BaseURL     string            `json:"baseUrl"`
	APIKey      string            `json:"apiKey,omitempty"`
	BearerToken string            `json:"bearerToken,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	ModelID     string            `json:"modelId,omitempty"`
	WireModel   string            `json:"wireModel,omitempty"`
}

type copilotSessionConfig struct {
	SessionID                      string                     `json:"sessionId,omitempty"`
	ClientName                     string                     `json:"clientName,omitempty"`
	Model                          string                     `json:"model,omitempty"`
	Provider                       *copilotWireProviderConfig `json:"provider,omitempty"`
	RequestPermission              *bool                      `json:"requestPermission,omitempty"`
	WorkingDirectory               string                     `json:"workingDirectory,omitempty"`
	Streaming                      *bool                      `json:"streaming,omitempty"`
	IncludeSubAgentStreamingEvents *bool                      `json:"includeSubAgentStreamingEvents,omitempty"`
	EnvValueMode                   string                     `json:"envValueMode,omitempty"`
}

type copilotPermissionResult struct {
	Kind  string `json:"kind"`
	Rules []any  `json:"rules,omitempty"`
}

func newCopilotSession(ctx context.Context, workDir, cliBin string, extraArgs []string, model, mode, resumeSessionID string, extraEnv []string, provider *copilotWireProviderConfig) (*copilotSession, error) {
	sessionCtx, cancel := context.WithCancel(ctx)

	args := append(append([]string{}, extraArgs...), "--headless", "--stdio", "--no-auto-update")

	slog.Debug("copilotSession: starting", "bin", cliBin, "args", args, "dir", workDir)

	child := exec.CommandContext(sessionCtx, cliBin, args...)
	child.Dir = workDir
	prepareCmdForKill(child)

	env := os.Environ()
	if len(extraEnv) > 0 {
		env = core.MergeEnv(env, extraEnv)
	}
	child.Env = env

	stdin, err := child.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("copilotSession: stdin pipe: %w", err)
	}

	stdout, err := child.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("copilotSession: stdout pipe: %w", err)
	}

	var stderrBuf bytes.Buffer
	child.Stderr = &stderrBuf

	if err := child.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("copilotSession: start: %w", err)
	}

	cs := &copilotSession{
		cmd:                child,
		rpc:                newRPCClient(stdin),
		reader:             newLSPReader(stdout),
		events:             make(chan core.Event, 64),
		mode:               mode,
		model:              model,
		provider:           provider,
		workDir:            workDir,
		ctx:                sessionCtx,
		cancel:             cancel,
		done:               make(chan struct{}),
		pendingPermissions: make(map[string]json.RawMessage),
		eventPermissions:   make(map[string]struct{}),
	}
	cs.alive.Store(true)
	cs.autoApprove.Store(mode == "bypassPermissions")
	if resumeSessionID != "" && resumeSessionID != core.ContinueSession {
		cs.sessionID.Store(resumeSessionID)
	}

	// Start reading loop
	go cs.readLoop(&stderrBuf)

	// Perform handshake: ping then create/resume session
	if err := cs.handshake(resumeSessionID); err != nil {
		_ = cs.Close()
		return nil, fmt.Errorf("copilotSession: handshake failed: %w", err)
	}

	return cs, nil
}

func (cs *copilotSession) handshake(resumeSessionID string) error {
	// Step 1: Ping
	_, pingCh := cs.rpc.call("ping", nil)
	select {
	case resp := <-pingCh:
		if resp.Error != nil {
			return fmt.Errorf("ping: %w", resp.Error)
		}
		slog.Debug("copilotSession: ping OK")
	case <-time.After(10 * time.Second):
		return fmt.Errorf("ping timeout")
	case <-cs.ctx.Done():
		return cs.ctx.Err()
	}

	// Step 2: Create or resume session
	if resumeSessionID != "" && resumeSessionID != core.ContinueSession {
		_, resumeCh := cs.rpc.call("session.resume", cs.sessionConfig(resumeSessionID))
		select {
		case resp := <-resumeCh:
			if resp.Error != nil {
				slog.Warn("copilotSession: resume failed, creating new session", "error", resp.Error)
				return cs.createSession()
			}
			cs.sessionID.Store(resumeSessionID)
			slog.Info("copilotSession: session resumed", "sessionId", resumeSessionID)
		case <-time.After(10 * time.Second):
			return fmt.Errorf("session.resume timeout")
		case <-cs.ctx.Done():
			return cs.ctx.Err()
		}
	} else {
		return cs.createSession()
	}
	return nil
}

func (cs *copilotSession) createSession() error {
	_, createCh := cs.rpc.call("session.create", cs.sessionConfig(newCopilotSessionID()))
	select {
	case resp := <-createCh:
		if resp.Error != nil {
			return fmt.Errorf("session.create: %w", resp.Error)
		}
		var result struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(resp.Result, &result); err != nil {
			return fmt.Errorf("session.create decode: %w", err)
		}
		cs.sessionID.Store(result.SessionID)
		slog.Info("copilotSession: session created", "sessionId", result.SessionID)
	case <-time.After(10 * time.Second):
		return fmt.Errorf("session.create timeout")
	case <-cs.ctx.Done():
		return cs.ctx.Err()
	}
	return nil
}

func (cs *copilotSession) sessionConfig(sessionID string) copilotSessionConfig {
	requestPermission := true
	streaming := true
	includeSubAgentStreaming := true
	return copilotSessionConfig{
		SessionID:                      sessionID,
		ClientName:                     "cc-connect",
		Model:                          strings.TrimSpace(cs.model),
		Provider:                       cs.provider,
		RequestPermission:              &requestPermission,
		WorkingDirectory:               cs.workDir,
		Streaming:                      &streaming,
		IncludeSubAgentStreamingEvents: &includeSubAgentStreaming,
		EnvValueMode:                   "direct",
	}
}

func newCopilotSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("cc-connect-%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func (cs *copilotSession) readLoop(stderrBuf *bytes.Buffer) {
	defer func() {
		cs.alive.Store(false)
		cs.rpc.cancelAll(fmt.Errorf("process exited"))

		// Wait for process exit
		if err := cs.cmd.Wait(); err != nil {
			stderrMsg := strings.TrimSpace(stderrBuf.String())
			if stderrMsg != "" {
				slog.Error("copilotSession: process failed", "error", err, "stderr", stderrMsg)
				evt := core.Event{Type: core.EventError, Error: fmt.Errorf("%s", stderrMsg)}
				select {
				case cs.events <- evt:
				case <-cs.ctx.Done():
				}
			}
		}
		close(cs.events)
		close(cs.done)
	}()

	for {
		body, err := cs.reader.readMessage()
		if err != nil {
			if cs.ctx.Err() != nil {
				return
			}
			slog.Error("copilotSession: read error", "error", err)
			return
		}

		cs.handleMessage(body)
	}
}

func (cs *copilotSession) handleMessage(body []byte) {
	var probe struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		slog.Debug("copilotSession: invalid JSON", "error", err)
		return
	}

	hasID := len(probe.ID) > 0 && string(probe.ID) != "null"
	hasMethod := probe.Method != ""

	// server-to-client request: has both id AND method
	if hasID && hasMethod {
		cs.handleServerRequest(probe.ID, probe.Method, body)
		return
	}

	// Response: has id but no method
	if hasID && !hasMethod {
		var resp jsonRPCResponse
		if err := json.Unmarshal(body, &resp); err == nil {
			cs.rpc.dispatch(&resp)
		}
		return
	}

	// Notification: no id
	var notif jsonRPCNotification
	if err := json.Unmarshal(body, &notif); err != nil {
		slog.Debug("copilotSession: failed to parse notification", "error", err)
		return
	}
	cs.handleNotification(notif)
}

// handleServerRequest handles server-to-client RPC requests (have both id and method).
func (cs *copilotSession) handleServerRequest(rpcID json.RawMessage, method string, body []byte) {
	slog.Debug("copilotSession: server-to-client request", "method", method)

	switch method {
	case "session.event":
		var req struct {
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			slog.Debug("copilotSession: failed to parse session.event request", "error", err)
			_ = cs.rpc.respond(rpcID, nil, &jsonRPCError{Code: -32602, Message: "invalid params"})
			return
		}
		cs.handleSessionEvent(req.Params)
		_ = cs.rpc.respond(rpcID, map[string]any{}, nil)

	case "permission.request":
		var req struct {
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			slog.Debug("copilotSession: failed to parse permission.request", "error", err)
			_ = cs.rpc.respond(rpcID, nil, &jsonRPCError{Code: -32602, Message: "invalid params"})
			return
		}
		permReq, err := parsePermissionRequest(req.Params)
		if err != nil || permReq.RequestID == "" {
			slog.Debug("copilotSession: failed to parse permission.request params", "error", err)
			_ = cs.rpc.respond(rpcID, nil, &jsonRPCError{Code: -32602, Message: "invalid params"})
			return
		}
		if permReq.Tool == "" {
			permReq.Tool = permReq.ToolName
		}
		if permReq.Input == nil {
			permReq.Input = permReq.Arguments
		}
		cs.pendingPermMu.Lock()
		cs.pendingPermissions[permReq.RequestID] = rpcID
		cs.pendingPermMu.Unlock()
		cs.handlePermissionRequest(req.Params)
	default:
		// Send method-not-found response for unhandled server-to-client requests
		_ = cs.rpc.respond(rpcID, nil, &jsonRPCError{Code: -32601, Message: "method not found"})
	}
}

func (cs *copilotSession) handleNotification(notif jsonRPCNotification) {
	slog.Debug("copilotSession: notification", "method", notif.Method)

	switch notif.Method {
	case "session.event":
		cs.handleSessionEvent(notif.Params)
	case "permission.request":
		// Copilot may send permission.request as a notification (no id).
		cs.handlePermissionRequest(notif.Params)
	default:
		slog.Debug("copilotSession: unhandled notification", "method", notif.Method)
	}
}

// sessionEvent wraps Copilot's session.event notification params.
type sessionEvent struct {
	SessionID string            `json:"sessionId"`
	Event     sessionEventInner `json:"event"`
}

type sessionEventInner struct {
	Type string          `json:"type"`
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
}

func (cs *copilotSession) handleSessionEvent(params json.RawMessage) {
	var evt sessionEvent
	if err := json.Unmarshal(params, &evt); err != nil {
		slog.Debug("copilotSession: failed to parse session.event", "error", err)
		return
	}

	eventType := evt.Event.Type
	if eventType == "" {
		eventType = evt.Event.Kind
	}
	eventType = normalizeCopilotEventType(eventType)

	slog.Debug("copilotSession: session event", "type", eventType, "sessionId", evt.SessionID)

	switch eventType {
	case "assistant.message_delta":
		content := copilotEventText(evt.Event.Data)
		if content != "" {
			e := core.Event{Type: core.EventText, Content: content}
			select {
			case cs.events <- e:
			case <-cs.ctx.Done():
			}
		}

	case "assistant.message":
		usage := copilotEventUsage(evt.Event.Data)
		if len(evt.Event.Data) > 0 {
			cs.addContextUsage(usage.inputTokens, usage.outputTokens)

			e := core.Event{
				Type:         core.EventResult,
				SessionID:    cs.CurrentSessionID(),
				Done:         true,
				OutputTokens: usage.outputTokens,
			}
			select {
			case cs.events <- e:
			case <-cs.ctx.Done():
			}
		}

	case "assistant.usage":
		usage := copilotEventUsage(evt.Event.Data)
		cs.addContextUsage(usage.inputTokens, usage.outputTokens)

	case "permission.requested":
		cs.handlePermissionRequestedEvent(evt.Event.Data)

	case "assistant.turn_start":
		slog.Debug("copilotSession: turn started")

	case "assistant.turn_end":
		slog.Debug("copilotSession: turn ended")

	case "session.idle":
		slog.Debug("copilotSession: session idle")

	case "session.skills_loaded", "session.mcp_servers_loaded", "session.tools_updated", "session.usage_info", "session.title_changed":
		slog.Debug("copilotSession: capabilities updated", "type", eventType)

	case "system.message":
		// System prompt notification, can be ignored
		slog.Debug("copilotSession: system message received")

	case "pending_messages.modified", "custom_agents_updated", "session.start", "assistant.message_start", "assistant.streaming_delta", "response", "messages_snapshot":
		// Known informational events; no action required
		slog.Debug("copilotSession: informational event", "type", eventType)

	default:
		slog.Debug("copilotSession: unhandled session event", "type", eventType)
	}
}

// permissionRequest represents Copilot's permission.request notification.
type permissionRequest struct {
	SessionID string         `json:"sessionId"`
	RequestID string         `json:"requestId"`
	Tool      string         `json:"tool"`
	ToolName  string         `json:"toolName"`
	Kind      string         `json:"kind"`
	Input     map[string]any `json:"input"`
	Arguments map[string]any `json:"arguments"`
}

func (cs *copilotSession) handlePermissionRequest(params json.RawMessage) {
	req, err := parsePermissionRequest(params)
	if err != nil {
		slog.Debug("copilotSession: failed to parse permission request", "error", err)
		return
	}
	cs.emitPermissionRequest(req)
}

func normalizeCopilotEventType(eventType string) string {
	switch eventType {
	case "assistant_message":
		return "assistant.message"
	case "assistant_usage":
		return "assistant.usage"
	case "assistant_turn_start":
		return "assistant.turn_start"
	case "assistant_turn_end":
		return "assistant.turn_end"
	case "assistant_message_start":
		return "assistant.message_start"
	case "assistant_message_delta":
		return "assistant.message_delta"
	case "assistant_streaming_delta":
		return "assistant.streaming_delta"
	}
	return eventType
}

func copilotEventText(raw json.RawMessage) string {
	var data struct {
		DeltaContent string `json:"deltaContent"`
		Content      string `json:"content"`
		Text         string `json:"text"`
		Delta        string `json:"delta"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return ""
	}
	for _, s := range []string{data.DeltaContent, data.Content, data.Text, data.Delta} {
		if s != "" {
			return s
		}
	}
	return ""
}

type copilotUsage struct {
	inputTokens  int
	outputTokens int
}

func copilotEventUsage(raw json.RawMessage) copilotUsage {
	var data struct {
		InputTokens      int `json:"inputTokens"`
		OutputTokens     int `json:"outputTokens"`
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		Usage            struct {
			InputTokens      int `json:"inputTokens"`
			OutputTokens     int `json:"outputTokens"`
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return copilotUsage{}
	}
	usage := copilotUsage{inputTokens: data.InputTokens, outputTokens: data.OutputTokens}
	if usage.inputTokens == 0 {
		usage.inputTokens = data.PromptTokens
	}
	if usage.outputTokens == 0 {
		usage.outputTokens = data.CompletionTokens
	}
	if usage.inputTokens == 0 {
		usage.inputTokens = data.Usage.InputTokens
	}
	if usage.inputTokens == 0 {
		usage.inputTokens = data.Usage.PromptTokens
	}
	if usage.outputTokens == 0 {
		usage.outputTokens = data.Usage.OutputTokens
	}
	if usage.outputTokens == 0 {
		usage.outputTokens = data.Usage.CompletionTokens
	}
	return usage
}

func (cs *copilotSession) addContextUsage(inputTokens, outputTokens int) {
	if inputTokens == 0 && outputTokens == 0 {
		return
	}
	cs.contextMu.Lock()
	defer cs.contextMu.Unlock()
	if cs.contextUsage == nil {
		cs.contextUsage = &core.ContextUsage{}
	}
	cs.contextUsage.OutputTokens += outputTokens
	cs.contextUsage.InputTokens += inputTokens
	cs.contextUsage.TotalTokens = cs.contextUsage.InputTokens + cs.contextUsage.OutputTokens
	cs.contextUsage.UsedTokens = cs.contextUsage.TotalTokens
}

func (cs *copilotSession) handlePermissionRequestedEvent(data json.RawMessage) {
	var evt struct {
		RequestID         string          `json:"requestId"`
		PermissionRequest json.RawMessage `json:"permissionRequest"`
	}
	if err := json.Unmarshal(data, &evt); err != nil || evt.RequestID == "" || len(evt.PermissionRequest) == 0 {
		slog.Debug("copilotSession: failed to parse permission.requested event", "error", err)
		return
	}

	req, err := parsePermissionRequest(evt.PermissionRequest)
	if err != nil {
		slog.Debug("copilotSession: failed to parse permission.requested payload", "error", err)
		return
	}
	req.RequestID = evt.RequestID
	if req.Input == nil {
		req.Input = rawJSONMap(evt.PermissionRequest)
	}

	cs.pendingPermMu.Lock()
	if cs.eventPermissions == nil {
		cs.eventPermissions = make(map[string]struct{})
	}
	cs.eventPermissions[req.RequestID] = struct{}{}
	cs.pendingPermMu.Unlock()

	cs.emitPermissionRequest(req)
}

func (cs *copilotSession) emitPermissionRequest(req permissionRequest) {
	if req.Tool == "" {
		req.Tool = req.ToolName
	}
	if req.Tool == "" {
		req.Tool = strings.ReplaceAll(req.Kind, "_", ".")
	}
	if req.Input == nil {
		req.Input = req.Arguments
	}

	slog.Info("copilotSession: permission request", "requestId", req.RequestID, "tool", req.Tool)

	if cs.autoApprove.Load() {
		slog.Debug("copilotSession: auto-approving", "requestId", req.RequestID, "tool", req.Tool)
		_ = cs.RespondPermission(req.RequestID, core.PermissionResult{
			Behavior:     "allow",
			UpdatedInput: req.Input,
		})
		return
	}

	inputSummary := summarizeToolInput(req.Tool, req.Input)
	evt := core.Event{
		Type:         core.EventPermissionRequest,
		RequestID:    req.RequestID,
		ToolName:     req.Tool,
		ToolInput:    inputSummary,
		ToolInputRaw: req.Input,
	}
	select {
	case cs.events <- evt:
	case <-cs.ctx.Done():
	}
}

func parsePermissionRequest(params json.RawMessage) (permissionRequest, error) {
	var req permissionRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return permissionRequest{}, err
	}
	if req.RequestID != "" || req.Tool != "" || req.ToolName != "" || req.Kind != "" {
		return req, nil
	}
	var wrapped struct {
		SessionID         string            `json:"sessionId"`
		PermissionRequest permissionRequest `json:"permissionRequest"`
	}
	if err := json.Unmarshal(params, &wrapped); err != nil {
		return permissionRequest{}, err
	}
	wrapped.PermissionRequest.SessionID = wrapped.SessionID
	return wrapped.PermissionRequest, nil
}

func rawJSONMap(raw json.RawMessage) map[string]any {
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func summarizeToolInput(tool string, input map[string]any) string {
	if input == nil {
		return ""
	}
	b, err := json.Marshal(input)
	if err != nil {
		return ""
	}
	s := string(b)
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

// Send sends a user message to the running Copilot process.
func (cs *copilotSession) Send(prompt string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if !cs.alive.Load() {
		return fmt.Errorf("session process is not running")
	}

	// Handle images: save to temp dir and append file references
	if len(images) > 0 {
		imgPaths, err := saveImagesToTempDir(cs.workDir, images)
		if err != nil {
			slog.Warn("copilotSession: failed to save images", "error", err)
		} else {
			prompt = core.AppendFileRefs(prompt, imgPaths)
		}
	}

	// Handle files
	if len(files) > 0 {
		filePaths := core.SaveFilesToDisk(cs.workDir, files)
		prompt = core.AppendFileRefs(prompt, filePaths)
	}

	sid := cs.CurrentSessionID()
	if sid == "" {
		return fmt.Errorf("no active session")
	}

	params := map[string]any{
		"sessionId": sid,
		"prompt":    prompt,
	}

	_, sendCh := cs.rpc.call("session.send", params)

	// Don't block - just validate the send was accepted
	go func() {
		select {
		case resp := <-sendCh:
			if resp.Error != nil {
				slog.Error("copilotSession: send failed", "error", resp.Error)
				if cs.alive.Load() {
					evt := core.Event{Type: core.EventError, Error: fmt.Errorf("send: %s", resp.Error.Message)}
					select {
					case cs.events <- evt:
					case <-cs.ctx.Done():
					}
				}
			} else {
				slog.Debug("copilotSession: send accepted")
			}
		case <-cs.ctx.Done():
		}
	}()

	return nil
}

// RespondPermission sends a permission decision back to the Copilot process.
// If the permission request came as a server-to-client RPC request (has a JSON-RPC id),
// a proper JSON-RPC response is sent; otherwise a notification is used.
func (cs *copilotSession) RespondPermission(requestID string, result core.PermissionResult) error {
	if !cs.alive.Load() {
		return fmt.Errorf("session process is not running")
	}

	decision := copilotPermissionResult{Kind: copilotPermissionKind(result)}

	// Check if this permission came as a server-to-client RPC request
	cs.pendingPermMu.Lock()
	rpcID, hasRPCID := cs.pendingPermissions[requestID]
	if hasRPCID {
		delete(cs.pendingPermissions, requestID)
	}
	_, hasEventPermission := cs.eventPermissions[requestID]
	if hasEventPermission {
		delete(cs.eventPermissions, requestID)
	}
	cs.pendingPermMu.Unlock()

	if hasRPCID {
		return cs.rpc.respond(rpcID, decision, nil)
	}
	if hasEventPermission {
		_, ch := cs.rpc.call("session.permissions.handlePendingPermissionRequest", map[string]any{
			"sessionId": cs.CurrentSessionID(),
			"requestId": requestID,
			"result":    decision,
		})
		go func() {
			select {
			case resp := <-ch:
				if resp.Error != nil {
					slog.Error("copilotSession: permission response failed", "error", resp.Error)
				}
			case <-cs.ctx.Done():
			}
		}()
		return nil
	}
	return cs.rpc.notify("permission.respond", map[string]any{
		"requestId": requestID,
		"result":    decision,
	})
}

func copilotPermissionKind(result core.PermissionResult) string {
	if result.Behavior == "allow" {
		return "approve-once"
	}
	if strings.TrimSpace(result.Message) == "denied: no active user turn" {
		return "user-not-available"
	}
	return "reject"
}

func (cs *copilotSession) Events() <-chan core.Event {
	return cs.events
}

func (cs *copilotSession) CurrentSessionID() string {
	v, _ := cs.sessionID.Load().(string)
	return v
}

func (cs *copilotSession) Alive() bool {
	return cs.alive.Load()
}

// GetContextUsage implements core.ContextUsageReporter.
func (cs *copilotSession) GetContextUsage() *core.ContextUsage {
	cs.contextMu.RLock()
	defer cs.contextMu.RUnlock()
	if cs.contextUsage == nil {
		return nil
	}
	cu := *cs.contextUsage
	return &cu
}

var (
	copilotGracefulTimeout = 3 * time.Second
	copilotSigtermWait     = 5 * time.Second
)

func (cs *copilotSession) Close() error {
	// Close stdin to signal EOF
	if w, ok := cs.rpc.writer.w.(io.Closer); ok {
		_ = w.Close()
	}

	select {
	case <-cs.done:
		slog.Info("copilotSession: exited cleanly after stdin close")
		return nil
	case <-time.After(copilotGracefulTimeout):
		slog.Warn("copilotSession: graceful stop timed out, sending SIGTERM")
	}

	terminateCmd(cs.cmd)

	select {
	case <-cs.done:
		slog.Info("copilotSession: exited after SIGTERM")
		return nil
	case <-time.After(copilotSigtermWait):
		slog.Warn("copilotSession: SIGTERM timed out, sending SIGKILL")
	}

	cs.cancel()
	_ = forceKillCmd(cs.cmd)
	<-cs.done
	return nil
}

// saveImagesToTempDir saves image attachments to a temp directory under workDir
// and returns their file paths for inclusion in the prompt.
func saveImagesToTempDir(workDir string, images []core.ImageAttachment) ([]string, error) {
	imgDir := filepath.Join(workDir, ".cc-connect", "images")
	if err := os.MkdirAll(imgDir, 0o755); err != nil {
		return nil, fmt.Errorf("saveImagesToTempDir: mkdir: %w", err)
	}

	paths := make([]string, 0, len(images))
	for i, img := range images {
		ext := imageExt(img.MimeType)
		fname := fmt.Sprintf("img_%d_%d%s", time.Now().UnixMilli(), i, ext)
		fpath := filepath.Join(imgDir, fname)
		if err := os.WriteFile(fpath, img.Data, 0o644); err != nil {
			return nil, fmt.Errorf("saveImagesToTempDir: write %s: %w", fname, err)
		}
		paths = append(paths, fpath)
	}
	return paths, nil
}

func imageExt(mimeType string) string {
	switch strings.ToLower(mimeType) {
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".jpg"
	}
}
