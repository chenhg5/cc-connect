package qoder

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

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

func TestHandleAssistant_SkipsNonFinished(t *testing.T) {
	qs := newTestSession()
	defer qs.cancel()

	// Neither status="finished" nor stop_reason set — should be skipped
	ev := &streamEvent{
		Type: "assistant",
		Message: &streamMessage{
			Status:  "tool_calling",
			Content: []byte(`[{"type":"text","text":"should be skipped"}]`),
		},
	}
	qs.handleEvent(ev)

	select {
	case got := <-qs.events:
		t.Errorf("expected no event, got type=%s content=%q", got.Type, got.Content)
	default:
		// ok
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
