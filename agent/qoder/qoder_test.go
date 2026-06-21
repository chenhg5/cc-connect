package qoder

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// TestAgent_StartSessionWorkDirRace exercises concurrent SetWorkDir + StartSession.
// Without the fix, StartSession reads a.workDir without holding a.mu while
// SetWorkDir writes it under the lock, which Go's -race detector flags as a
// data race. With the fix, the field is captured inside the existing critical
// section and no race is reported.
//
// newQoderSession only initialises the session struct; it does not spawn the
// qodercli binary until Send() is called, so this test runs without requiring
// the CLI on PATH.
func TestAgent_StartSessionWorkDirRace(t *testing.T) {
	a := &Agent{workDir: "/initial"}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			a.SetWorkDir(fmt.Sprintf("/path-%d", i))
		}(i)
		go func() {
			defer wg.Done()
			sess, err := a.StartSession(context.Background(), "")
			if err != nil {
				t.Errorf("StartSession: %v", err)
				return
			}
			_ = sess.Close()
		}()
	}
	wg.Wait()
}

func TestQoderSession(t *testing.T) {
	if os.Getenv("QODER_INTEGRATION") == "" {
		t.Skip("set QODER_INTEGRATION=1 to run")
	}

	agent, err := New(map[string]any{
		"work_dir": "/tmp",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	sess, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer sess.Close()

	if err := sess.Send("say hello in one word", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	timeout := time.After(30 * time.Second)
	var gotResult bool
	for !gotResult {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				t.Fatal("events channel closed prematurely")
			}
			switch ev.Type {
			case core.EventText:
				fmt.Printf("[TEXT] %s\n", ev.Content)
			case core.EventToolUse:
				fmt.Printf("[TOOL] %s: %s\n", ev.ToolName, ev.ToolInput)
			case core.EventResult:
				fmt.Printf("[RESULT] sid=%s content=%s\n", ev.SessionID, ev.Content)
				gotResult = true
			case core.EventError:
				t.Fatalf("[ERROR] %v", ev.Error)
			default:
				fmt.Printf("[%s] %s\n", ev.Type, ev.Content)
			}
		case <-timeout:
			t.Fatal("timeout waiting for result")
		}
	}

	sid := sess.CurrentSessionID()
	if sid == "" {
		t.Error("expected a session ID from init event")
	}
	fmt.Printf("Session ID: %s\n", sid)
}

// Unit tests that don't require real CLI

func TestNormalizeMode(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"yolo", "yolo"},
		{"YOLO", "yolo"},
		{"bypass", "yolo"},
		{"dangerously-skip-permissions", "yolo"},
		{"default", "default"},
		{"", "default"},
		{"unknown", "default"},
		{"  yolo  ", "yolo"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeMode(tt.input)
			if got != tt.expected {
				t.Errorf("normalizeMode(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestAgent_Name(t *testing.T) {
	a := &Agent{}
	if got := a.Name(); got != "qoder" {
		t.Errorf("Name() = %q, want %q", got, "qoder")
	}
}

func TestAgent_CLIBinaryName(t *testing.T) {
	a := &Agent{}
	if got := a.CLIBinaryName(); got != "qodercli" {
		t.Errorf("CLIBinaryName() = %q, want %q", got, "qodercli")
	}
}

func TestAgent_CLIDisplayName(t *testing.T) {
	a := &Agent{}
	if got := a.CLIDisplayName(); got != "Qoder" {
		t.Errorf("CLIDisplayName() = %q, want %q", got, "Qoder")
	}
}

func TestAgent_SetWorkDir(t *testing.T) {
	a := &Agent{}
	a.SetWorkDir("/tmp/test")
	if got := a.GetWorkDir(); got != "/tmp/test" {
		t.Errorf("GetWorkDir() = %q, want %q", got, "/tmp/test")
	}
}

func TestAgent_SetModel(t *testing.T) {
	a := &Agent{}
	a.SetModel("gpt-4")
	a.mu.Lock()
	got := a.model
	a.mu.Unlock()
	if got != "gpt-4" {
		t.Errorf("model = %q, want %q", got, "gpt-4")
	}
}

// verify Agent implements core.Agent
var _ core.Agent = (*Agent)(nil)

// ── handleEvent unit tests (old vs new qodercli format) ──

func newTestSession() *qoderSession {
	ctx, cancel := context.WithCancel(context.Background())
	qs := &qoderSession{
		events: make(chan core.Event, 64),
		ctx:    ctx,
		cancel: cancel,
	}
	qs.alive.Store(true)
	return qs
}

func TestHandleAssistant_OldFormat(t *testing.T) {
	qs := newTestSession()
	defer qs.cancel()

	ev := &streamEvent{
		Type:      "assistant",
		SessionID: "old-session-1",
		Message: &streamMessage{
			Status:  "finished",
			Content: []byte(`[{"type":"text","text":"hello old"}]`),
		},
	}
	qs.handleEvent(ev)

	select {
	case got := <-qs.events:
		if got.Type != core.EventText || got.Content != "hello old" {
			t.Errorf("got type=%s content=%q, want EventText/hello old", got.Type, got.Content)
		}
	default:
		t.Error("expected a text event but channel was empty")
	}
}

func TestHandleAssistant_NewFormat(t *testing.T) {
	qs := newTestSession()
	defer qs.cancel()

	ev := &streamEvent{
		Type:      "assistant",
		SessionID: "new-session-1",
		Message: &streamMessage{
			StopReason: "end_turn",
			Content:    []byte(`[{"type":"text","text":"hello new"}]`),
		},
	}
	qs.handleEvent(ev)

	select {
	case got := <-qs.events:
		if got.Type != core.EventText || got.Content != "hello new" {
			t.Errorf("got type=%s content=%q, want EventText/hello new", got.Type, got.Content)
		}
	default:
		t.Error("expected a text event but channel was empty")
	}
}

func TestHandleAssistant_ToolUseStopReason(t *testing.T) {
	qs := newTestSession()
	defer qs.cancel()

	ev := &streamEvent{
		Type: "assistant",
		Message: &streamMessage{
			StopReason: "tool_use",
			Content:    []byte(`[{"type":"function","name":"Bash","input":"{\"command\":\"ls\"}"}]`),
		},
	}
	qs.handleEvent(ev)

	select {
	case got := <-qs.events:
		if got.Type != core.EventToolUse || got.ToolName != "Bash" {
			t.Errorf("got type=%s tool=%s, want EventToolUse/Bash", got.Type, got.ToolName)
		}
	default:
		t.Error("expected a tool_use event but channel was empty")
	}
}

func TestHandleAssistant_EmitsNonFinishedText(t *testing.T) {
	qs := newTestSession()
	defer qs.cancel()

	// Newer qodercli stream-json can emit partial text before the final frame.
	ev := &streamEvent{
		Type: "assistant",
		Message: &streamMessage{
			ID:      "msg-partial",
			Status:  "streaming",
			Content: []byte(`[{"type":"text","text":"partial"}]`),
		},
	}
	qs.handleEvent(ev)

	select {
	case got := <-qs.events:
		if got.Type != core.EventText || got.Content != "partial" {
			t.Errorf("got type=%s content=%q, want EventText/partial", got.Type, got.Content)
		}
	default:
		t.Error("expected a text event but channel was empty")
	}
}

func TestHandleAssistant_ThinkingDoesNotBlockTextOnSameID(t *testing.T) {
	qs := newTestSession()
	defer qs.cancel()

	thinking := &streamEvent{
		Type: "assistant",
		Message: &streamMessage{
			ID:      "msg-same-id",
			Content: []byte(`[{"type":"thinking","thinking":"hidden"},{"type":"redacted_thinking","data":"secret"}]`),
		},
	}
	text := &streamEvent{
		Type: "assistant",
		Message: &streamMessage{
			ID:         "msg-same-id",
			StopReason: "end_turn",
			Content:    []byte(`[{"type":"text","text":"visible answer"}]`),
		},
	}

	qs.handleEvent(thinking)
	select {
	case got := <-qs.events:
		t.Fatalf("expected thinking frames to be skipped, got type=%s content=%q", got.Type, got.Content)
	default:
	}

	qs.handleEvent(text)
	select {
	case got := <-qs.events:
		if got.Type != core.EventText || got.Content != "visible answer" {
			t.Errorf("got type=%s content=%q, want EventText/visible answer", got.Type, got.Content)
		}
	default:
		t.Error("expected text after thinking frames")
	}
}

func TestHandleAssistant_DeduplicatesCumulativeTextForSameMessageID(t *testing.T) {
	qs := newTestSession()
	defer qs.cancel()

	events := []*streamEvent{
		{
			Type: "assistant",
			Message: &streamMessage{
				ID:      "msg-cumulative",
				Status:  "streaming",
				Content: []byte(`[{"type":"text","text":"Hello"}]`),
			},
		},
		{
			Type: "assistant",
			Message: &streamMessage{
				ID:      "msg-cumulative",
				Status:  "streaming",
				Content: []byte(`[{"type":"text","text":"Hello world"}]`),
			},
		},
		{
			Type: "assistant",
			Message: &streamMessage{
				ID:         "msg-cumulative",
				StopReason: "end_turn",
				Content:    []byte(`[{"type":"text","text":"Hello world"}]`),
			},
		},
	}
	for _, ev := range events {
		qs.handleEvent(ev)
	}

	got := drainQoderEvents(qs)
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2: %#v", len(got), got)
	}
	if got[0].Type != core.EventText || got[0].Content != "Hello" {
		t.Errorf("event 0 = %s %q, want EventText Hello", got[0].Type, got[0].Content)
	}
	if got[1].Type != core.EventText || got[1].Content != " world" {
		t.Errorf("event 1 = %s %q, want EventText ' world'", got[1].Type, got[1].Content)
	}
}

func TestEmitAssistantTextBoundsDedupCache(t *testing.T) {
	qs := newTestSession()
	defer qs.cancel()

	for i := 0; i < maxAssistantTextCacheEntries+1; i++ {
		if !qs.emitAssistantText(fmt.Sprintf("msg-%d", i), "chunk") {
			t.Fatalf("emitAssistantText(%d) returned false", i)
		}
		select {
		case <-qs.events:
		default:
			t.Fatalf("expected emitted chunk for message %d", i)
		}
	}

	qs.textMu.Lock()
	defer qs.textMu.Unlock()
	if got := len(qs.assistantTextByID); got != maxAssistantTextCacheEntries {
		t.Fatalf("assistantTextByID len = %d, want %d", got, maxAssistantTextCacheEntries)
	}
	if _, ok := qs.assistantTextByID["msg-0"]; ok {
		t.Fatal("oldest cached message id was not evicted")
	}
	if _, ok := qs.assistantTextByID[fmt.Sprintf("msg-%d", maxAssistantTextCacheEntries)]; !ok {
		t.Fatal("newest cached message id was not retained")
	}
}

func TestHandleResult_OldFormat(t *testing.T) {
	qs := newTestSession()
	defer qs.cancel()

	ev := &streamEvent{
		Type:      "result",
		SessionID: "old-session-1",
		Message: &streamMessage{
			Content: []byte(`[{"type":"text","text":"result old"}]`),
		},
	}
	qs.handleEvent(ev)

	select {
	case got := <-qs.events:
		if got.Type != core.EventResult || got.Content != "result old" {
			t.Errorf("got type=%s content=%q, want EventResult/result old", got.Type, got.Content)
		}
	default:
		t.Error("expected a result event but channel was empty")
	}
}

func TestHandleResult_NewFormat(t *testing.T) {
	qs := newTestSession()
	defer qs.cancel()

	// 0.2.x: message is nil, result text in top-level field
	ev := &streamEvent{
		Type:      "result",
		SessionID: "new-session-1",
		Result:    "result new",
	}
	qs.handleEvent(ev)

	select {
	case got := <-qs.events:
		if got.Type != core.EventResult || got.Content != "result new" {
			t.Errorf("got type=%s content=%q, want EventResult/result new", got.Type, got.Content)
		}
	default:
		t.Error("expected a result event but channel was empty")
	}
}

func TestHandleResult_OldFormatTakesPriority(t *testing.T) {
	qs := newTestSession()
	defer qs.cancel()

	// If both message.content and top-level result exist, message.content wins
	ev := &streamEvent{
		Type:   "result",
		Result: "fallback text",
		Message: &streamMessage{
			Content: []byte(`[{"type":"text","text":"primary text"}]`),
		},
	}
	qs.handleEvent(ev)

	select {
	case got := <-qs.events:
		if got.Content != "primary text" {
			t.Errorf("got content=%q, want primary text", got.Content)
		}
	default:
		t.Error("expected a result event but channel was empty")
	}
}

func drainQoderEvents(qs *qoderSession) []core.Event {
	var events []core.Event
	for {
		select {
		case ev := <-qs.events:
			events = append(events, ev)
		default:
			return events
		}
	}
}
