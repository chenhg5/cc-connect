package copilot

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestHandleSessionEvent_AssistantMessage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cs := &copilotSession{
		events: make(chan core.Event, 10),
		ctx:    ctx,
		cancel: cancel,
	}
	cs.sessionID.Store("test-session")
	cs.alive.Store(true)

	data, _ := json.Marshal(map[string]any{
		"content":      "Hello, world!",
		"outputTokens": 42,
		"inputTokens":  100,
	})
	cs.handleSessionEvent(json.RawMessage(mustMarshal(t, sessionEvent{
		SessionID: "test-session",
		Event:     sessionEventInner{Type: "assistant.message", Data: data},
	})))

	select {
	case evt := <-cs.events:
		if evt.Type != core.EventResult {
			t.Fatalf("event type = %v, want EventResult", evt.Type)
		}
		if evt.Content != "" {
			t.Fatalf("content = %q, want empty final content because text was streamed", evt.Content)
		}
		if evt.OutputTokens != 42 {
			t.Fatalf("outputTokens = %d, want 42", evt.OutputTokens)
		}
	default:
		t.Fatal("no event emitted")
	}

	// Verify context usage was updated
	usage := cs.GetContextUsage()
	if usage == nil {
		t.Fatal("context usage is nil")
	}
	if usage.OutputTokens != 42 {
		t.Fatalf("usage.OutputTokens = %d, want 42", usage.OutputTokens)
	}
	if usage.InputTokens != 100 {
		t.Fatalf("usage.InputTokens = %d, want 100", usage.InputTokens)
	}
	if usage.TotalTokens != 142 {
		t.Fatalf("usage.TotalTokens = %d, want 142", usage.TotalTokens)
	}
}

func TestHandleSessionEvent_MessageDelta(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cs := &copilotSession{
		events: make(chan core.Event, 10),
		ctx:    ctx,
		cancel: cancel,
	}
	cs.alive.Store(true)

	data, _ := json.Marshal(map[string]any{
		"deltaContent": "chunk",
	})
	cs.handleSessionEvent(json.RawMessage(mustMarshal(t, sessionEvent{
		Event: sessionEventInner{Type: "assistant.message_delta", Data: data},
	})))

	select {
	case evt := <-cs.events:
		if evt.Type != core.EventText {
			t.Fatalf("event type = %v, want EventText", evt.Type)
		}
		if evt.Content != "chunk" {
			t.Fatalf("content = %q, want 'chunk'", evt.Content)
		}
	default:
		t.Fatal("no event emitted")
	}
}

func TestHandleSessionEvent_KindAndCurrentCopilotEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cs := &copilotSession{events: make(chan core.Event, 10), ctx: ctx, cancel: cancel}
	cs.sessionID.Store("test-session")

	delta, _ := json.Marshal(map[string]any{"content": "integration-ok"})
	cs.handleSessionEvent(json.RawMessage(mustMarshal(t, sessionEvent{
		SessionID: "test-session",
		Event:     sessionEventInner{Kind: "assistant.message_delta", Data: delta},
	})))
	select {
	case evt := <-cs.events:
		if evt.Type != core.EventText || evt.Content != "integration-ok" {
			t.Fatalf("event = %+v, want integration text", evt)
		}
	default:
		t.Fatal("no streaming event emitted")
	}

	usage, _ := json.Marshal(map[string]any{"usage": map[string]any{"prompt_tokens": 12, "completion_tokens": 3}})
	cs.handleSessionEvent(json.RawMessage(mustMarshal(t, sessionEvent{
		SessionID: "test-session",
		Event:     sessionEventInner{Kind: "assistant.usage", Data: usage},
	})))

	final, _ := json.Marshal(map[string]any{"content": "integration-ok"})
	cs.handleSessionEvent(json.RawMessage(mustMarshal(t, sessionEvent{
		SessionID: "test-session",
		Event:     sessionEventInner{Kind: "assistant.message", Data: final},
	})))
	select {
	case evt := <-cs.events:
		if evt.Type != core.EventResult || !evt.Done {
			t.Fatalf("event = %+v, want done result", evt)
		}
	default:
		t.Fatal("no final event emitted")
	}

	got := cs.GetContextUsage()
	if got == nil || got.InputTokens != 12 || got.OutputTokens != 3 {
		t.Fatalf("usage = %+v, want 12 input and 3 output", got)
	}
}

func TestHandlePermissionRequest_AutoApprove(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cs := &copilotSession{
		events: make(chan core.Event, 10),
		ctx:    ctx,
		cancel: cancel,
	}
	cs.alive.Store(true)
	cs.autoApprove.Store(true)
	// rpc needs to be set for RespondPermission to work - but in auto-approve
	// mode we call notify which writes to rpc.writer. Use a no-op writer.
	var nopBuf nopWriter
	cs.rpc = newRPCClient(&nopBuf)

	params, _ := json.Marshal(permissionRequest{
		RequestID: "req-1",
		Tool:      "shell",
		Input:     map[string]any{"command": "ls"},
	})
	cs.handlePermissionRequest(params)

	// In auto-approve mode, no event should be forwarded to the engine
	select {
	case evt := <-cs.events:
		t.Fatalf("unexpected event in auto-approve mode: %v", evt)
	default:
		// expected - no event
	}
}

func TestHandlePermissionRequest_AskUser(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cs := &copilotSession{
		events: make(chan core.Event, 10),
		ctx:    ctx,
		cancel: cancel,
	}
	cs.alive.Store(true)
	cs.autoApprove.Store(false)

	params, _ := json.Marshal(permissionRequest{
		RequestID: "req-2",
		Tool:      "file_write",
		Input:     map[string]any{"path": "/tmp/test.txt"},
	})
	cs.handlePermissionRequest(params)

	select {
	case evt := <-cs.events:
		if evt.Type != core.EventPermissionRequest {
			t.Fatalf("event type = %v, want EventPermissionRequest", evt.Type)
		}
		if evt.RequestID != "req-2" {
			t.Fatalf("requestID = %q, want req-2", evt.RequestID)
		}
		if evt.ToolName != "file_write" {
			t.Fatalf("toolName = %q, want file_write", evt.ToolName)
		}
	default:
		t.Fatal("no event emitted for permission request")
	}
}

func TestHandlePermissionRequest_WrappedCopilotShape(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cs := &copilotSession{
		events: make(chan core.Event, 10),
		ctx:    ctx,
		cancel: cancel,
	}
	cs.alive.Store(true)

	params, _ := json.Marshal(map[string]any{
		"sessionId": "sess-1",
		"permissionRequest": map[string]any{
			"requestId": "req-3",
			"toolName":  "write",
			"arguments": map[string]any{"path": "README.md"},
		},
	})
	cs.handlePermissionRequest(params)

	select {
	case evt := <-cs.events:
		if evt.RequestID != "req-3" {
			t.Fatalf("requestID = %q, want req-3", evt.RequestID)
		}
		if evt.ToolName != "write" {
			t.Fatalf("toolName = %q, want write", evt.ToolName)
		}
		if evt.ToolInputRaw["path"] != "README.md" {
			t.Fatalf("raw input = %v, want path README.md", evt.ToolInputRaw)
		}
	default:
		t.Fatal("no event emitted for wrapped permission request")
	}
}

func TestHandleServerRequest_PermissionRequestWrappedCopilotShape(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var buf bytes.Buffer
	cs := &copilotSession{
		events:             make(chan core.Event, 10),
		ctx:                ctx,
		cancel:             cancel,
		rpc:                newRPCClient(&buf),
		pendingPermissions: make(map[string]json.RawMessage),
	}
	cs.alive.Store(true)

	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      11,
		"method":  "permission.request",
		"params": map[string]any{
			"sessionId": "sess-1",
			"permissionRequest": map[string]any{
				"requestId": "req-4",
				"toolName":  "shell",
				"arguments": map[string]any{"command": "pwd"},
			},
		},
	})
	cs.handleServerRequest(json.RawMessage(`11`), "permission.request", body)

	select {
	case evt := <-cs.events:
		if evt.RequestID != "req-4" {
			t.Fatalf("requestID = %q, want req-4", evt.RequestID)
		}
		if evt.ToolName != "shell" {
			t.Fatalf("toolName = %q, want shell", evt.ToolName)
		}
	default:
		t.Fatal("no event emitted for server permission request")
	}
	cs.pendingPermMu.Lock()
	_, ok := cs.pendingPermissions["req-4"]
	cs.pendingPermMu.Unlock()
	if !ok {
		t.Fatal("pending permission req-4 not recorded")
	}
}

func TestHandleSessionEvent_PermissionRequestedEvent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cs := &copilotSession{
		events:           make(chan core.Event, 10),
		ctx:              ctx,
		cancel:           cancel,
		eventPermissions: make(map[string]struct{}),
	}
	cs.alive.Store(true)

	data, _ := json.Marshal(map[string]any{
		"requestId": "req-event-1",
		"permissionRequest": map[string]any{
			"kind":    "shell",
			"command": "pwd",
		},
	})
	cs.handleSessionEvent(json.RawMessage(mustMarshal(t, sessionEvent{
		SessionID: "sess-1",
		Event:     sessionEventInner{Type: "permission.requested", Data: data},
	})))

	select {
	case evt := <-cs.events:
		if evt.Type != core.EventPermissionRequest {
			t.Fatalf("event type = %v, want EventPermissionRequest", evt.Type)
		}
		if evt.RequestID != "req-event-1" {
			t.Fatalf("requestID = %q, want req-event-1", evt.RequestID)
		}
		if evt.ToolName != "shell" {
			t.Fatalf("toolName = %q, want shell", evt.ToolName)
		}
		if evt.ToolInputRaw["command"] != "pwd" {
			t.Fatalf("raw input = %v, want command pwd", evt.ToolInputRaw)
		}
	default:
		t.Fatal("no event emitted for permission.requested")
	}

	cs.pendingPermMu.Lock()
	_, ok := cs.eventPermissions["req-event-1"]
	cs.pendingPermMu.Unlock()
	if !ok {
		t.Fatal("event permission req-event-1 not recorded")
	}
}

func TestRespondPermission_EventPermissionUsesSessionScopedRPC(t *testing.T) {
	var buf bytes.Buffer
	cs := &copilotSession{
		rpc:              newRPCClient(&buf),
		eventPermissions: map[string]struct{}{"req-event-2": {}},
		ctx:              context.Background(),
	}
	cs.alive.Store(true)
	cs.sessionID.Store("sess-2")

	if err := cs.RespondPermission("req-event-2", core.PermissionResult{Behavior: "allow"}); err != nil {
		t.Fatalf("RespondPermission: %v", err)
	}

	_, body := decodeFramedMessage(t, buf.String())
	var req struct {
		Method string `json:"method"`
		Params struct {
			SessionID string `json:"sessionId"`
			RequestID string `json:"requestId"`
			Result    struct {
				Kind string `json:"kind"`
			} `json:"result"`
		} `json:"params"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if req.Method != "session.permissions.handlePendingPermissionRequest" {
		t.Fatalf("method = %q", req.Method)
	}
	if req.Params.SessionID != "sess-2" || req.Params.RequestID != "req-event-2" {
		t.Fatalf("params = %+v", req.Params)
	}
	if req.Params.Result.Kind != "approve-once" {
		t.Fatalf("kind = %q, want approve-once", req.Params.Result.Kind)
	}
}

func TestSessionConfig_MatchesCopilotCreateResumeShape(t *testing.T) {
	cs := &copilotSession{
		model:   "gpt-5.2",
		workDir: "/work/project",
		provider: &copilotWireProviderConfig{
			Type:      "anthropic",
			BaseURL:   "https://api.anthropic.com",
			APIKey:    "sk-ant",
			ModelID:   "claude-sonnet-4.6",
			WireAPI:   "responses",
			Headers:   map[string]string{"X-Test": "1"},
			WireModel: "deployment-a",
		},
	}
	cfg := cs.sessionConfig("sess-1")
	if cfg.SessionID != "sess-1" {
		t.Fatalf("SessionID = %q, want sess-1", cfg.SessionID)
	}
	if cfg.ClientName != "cc-connect" {
		t.Fatalf("ClientName = %q, want cc-connect", cfg.ClientName)
	}
	if cfg.Model != "gpt-5.2" {
		t.Fatalf("Model = %q, want gpt-5.2", cfg.Model)
	}
	if cfg.WorkingDirectory != "/work/project" {
		t.Fatalf("WorkingDirectory = %q, want /work/project", cfg.WorkingDirectory)
	}
	if cfg.RequestPermission == nil || !*cfg.RequestPermission {
		t.Fatalf("RequestPermission = %v, want true", cfg.RequestPermission)
	}
	if cfg.Streaming == nil || !*cfg.Streaming {
		t.Fatalf("Streaming = %v, want true", cfg.Streaming)
	}
	if cfg.IncludeSubAgentStreamingEvents == nil || !*cfg.IncludeSubAgentStreamingEvents {
		t.Fatalf("IncludeSubAgentStreamingEvents = %v, want true", cfg.IncludeSubAgentStreamingEvents)
	}
	if cfg.EnvValueMode != "direct" {
		t.Fatalf("EnvValueMode = %q, want direct", cfg.EnvValueMode)
	}
	if cfg.Provider == nil || cfg.Provider.Type != "anthropic" || cfg.Provider.WireModel != "deployment-a" {
		t.Fatalf("Provider = %+v, want anthropic/deployment-a", cfg.Provider)
	}
}

func TestRespondPermission_RPCUsesCopilotResultShape(t *testing.T) {
	var buf bytes.Buffer
	cs := &copilotSession{
		rpc:                newRPCClient(&buf),
		pendingPermissions: map[string]json.RawMessage{"req-1": json.RawMessage(`7`)},
	}
	cs.alive.Store(true)

	if err := cs.RespondPermission("req-1", core.PermissionResult{Behavior: "allow"}); err != nil {
		t.Fatalf("RespondPermission: %v", err)
	}

	_, body := decodeFramedMessage(t, buf.String())
	var resp struct {
		ID     int `json:"id"`
		Result struct {
			Kind string `json:"kind"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.ID != 7 {
		t.Fatalf("id = %d, want 7", resp.ID)
	}
	if resp.Result.Kind != "approve-once" {
		t.Fatalf("kind = %q, want approve-once", resp.Result.Kind)
	}
}

func TestRespondPermission_NotifyUsesCopilotResultShape(t *testing.T) {
	var buf bytes.Buffer
	cs := &copilotSession{rpc: newRPCClient(&buf)}
	cs.alive.Store(true)

	if err := cs.RespondPermission("req-2", core.PermissionResult{Behavior: "deny", Message: "no"}); err != nil {
		t.Fatalf("RespondPermission: %v", err)
	}

	_, body := decodeFramedMessage(t, buf.String())
	var notif struct {
		Method string `json:"method"`
		Params struct {
			RequestID string `json:"requestId"`
			Result    struct {
				Kind string `json:"kind"`
			} `json:"result"`
		} `json:"params"`
	}
	if err := json.Unmarshal([]byte(body), &notif); err != nil {
		t.Fatalf("unmarshal notification: %v", err)
	}
	if notif.Method != "permission.respond" {
		t.Fatalf("method = %q, want permission.respond", notif.Method)
	}
	if notif.Params.RequestID != "req-2" {
		t.Fatalf("requestID = %q, want req-2", notif.Params.RequestID)
	}
	if notif.Params.Result.Kind != "reject" {
		t.Fatalf("kind = %q, want reject", notif.Params.Result.Kind)
	}
}

func TestCopilotSession_CurrentSessionID(t *testing.T) {
	cs := &copilotSession{}
	if got := cs.CurrentSessionID(); got != "" {
		t.Fatalf("CurrentSessionID() = %q, want empty", got)
	}
	cs.sessionID.Store("abc-123")
	if got := cs.CurrentSessionID(); got != "abc-123" {
		t.Fatalf("CurrentSessionID() = %q, want abc-123", got)
	}
}

func TestCopilotSession_GetContextUsage_Nil(t *testing.T) {
	cs := &copilotSession{}
	if got := cs.GetContextUsage(); got != nil {
		t.Fatalf("GetContextUsage() = %v, want nil", got)
	}
}

// helpers

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func decodeFramedMessage(t *testing.T, frame string) (string, string) {
	t.Helper()
	parts := bytes.SplitN([]byte(frame), []byte("\r\n\r\n"), 2)
	if len(parts) != 2 {
		t.Fatalf("frame missing header separator: %q", frame)
	}
	return string(parts[0]), string(parts[1])
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestSession_SendWithFiles(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tmpDir := t.TempDir()
	var nopBuf nopWriter
	cs := &copilotSession{
		events:  make(chan core.Event, 10),
		ctx:     ctx,
		cancel:  cancel,
		workDir: tmpDir,
		rpc:     newRPCClient(&nopBuf),
	}
	cs.alive.Store(true)
	cs.sessionID.Store("test-session")

	files := []core.FileAttachment{
		{FileName: "test.txt", Data: []byte("hello file")},
	}

	// Send returns error because rpc.call tries to write to nopBuf then
	// the goroutine gets a nil channel. Just check it doesn't panic.
	// The file should still be saved before Send tries to contact the process.
	_ = cs.Send("please review", nil, files)
}

func TestSession_SendWithImages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tmpDir := t.TempDir()
	var nopBuf nopWriter
	cs := &copilotSession{
		events:  make(chan core.Event, 10),
		ctx:     ctx,
		cancel:  cancel,
		workDir: tmpDir,
		rpc:     newRPCClient(&nopBuf),
	}
	cs.alive.Store(true)
	cs.sessionID.Store("test-session")

	images := []core.ImageAttachment{
		{MimeType: "image/png", Data: []byte{0x89, 0x50, 0x4e, 0x47}},
	}

	// Just ensure no panic and images dir gets created
	_ = cs.Send("describe image", images, nil)
	imgDir := tmpDir + "/.cc-connect/images"
	entries, _ := os.ReadDir(imgDir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 image file, got %d", len(entries))
	}
}

func TestSaveImagesToTempDir(t *testing.T) {
	tmpDir := t.TempDir()
	images := []core.ImageAttachment{
		{MimeType: "image/jpeg", Data: []byte{0xff, 0xd8}},
		{MimeType: "image/png", Data: []byte{0x89, 0x50}},
		{MimeType: "image/webp", Data: []byte{0x52, 0x49}},
		{MimeType: "image/gif", Data: []byte{0x47, 0x49}},
	}

	paths, err := saveImagesToTempDir(tmpDir, images)
	if err != nil {
		t.Fatalf("saveImagesToTempDir() error = %v", err)
	}
	if len(paths) != 4 {
		t.Fatalf("expected 4 paths, got %d", len(paths))
	}

	exts := []string{".jpg", ".png", ".webp", ".gif"}
	for i, p := range paths {
		if filepath.Ext(p) != exts[i] {
			t.Errorf("path[%d] ext = %q, want %q", i, filepath.Ext(p), exts[i])
		}
		if _, err := os.Stat(p); err != nil {
			t.Errorf("file %s not found: %v", p, err)
		}
	}
}
