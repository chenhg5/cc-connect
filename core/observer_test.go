package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestObserverTargetInterface(t *testing.T) {
	// Verify the interface exists and has the right method
	var _ ObserverTarget = (*mockObserverTarget)(nil)
}

type mockObserverTarget struct{}

func (m *mockObserverTarget) SendObservation(ctx context.Context, channelID, text string) error {
	return nil
}

func TestParseObservationLine(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		wantType string
		wantText string
		wantSkip bool
	}{
		{
			name:     "user message",
			line:     `{"type":"user","message":{"role":"user","content":"hello world"},"entrypoint":"cli"}`,
			wantType: "user",
			wantText: "hello world",
		},
		{
			name:     "assistant text",
			line:     `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi there"}]},"entrypoint":"cli"}`,
			wantType: "assistant",
			wantText: "hi there",
		},
		{
			name:     "sdk-cli session skipped",
			line:     `{"type":"user","message":{"role":"user","content":"hello"},"entrypoint":"sdk-cli"}`,
			wantSkip: true,
		},
		{
			name:     "tool_use skipped",
			line:     `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash"}]},"entrypoint":"cli"}`,
			wantType: "assistant",
			wantText: "",
		},
		{
			name:     "non-message type skipped",
			line:     `{"type":"system","sessionId":"abc123"}`,
			wantSkip: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obs := parseObservationLine([]byte(tt.line))
			if tt.wantSkip {
				if obs != nil {
					t.Fatalf("expected nil, got %+v", obs)
				}
				return
			}
			if obs == nil {
				t.Fatal("expected non-nil observation")
			}
			if obs.role != tt.wantType {
				t.Fatalf("role: got %q, want %q", obs.role, tt.wantType)
			}
			if obs.text != tt.wantText {
				t.Fatalf("text: got %q, want %q", obs.text, tt.wantText)
			}
		})
	}
}

func TestSessionObserverPoll(t *testing.T) {
	dir := t.TempDir()

	var received []string
	var mu sync.Mutex
	target := &mockObserverTargetCapture{
		fn: func(ctx context.Context, channelID, text string) error {
			mu.Lock()
			received = append(received, text)
			mu.Unlock()
			return nil
		},
	}

	obs := newSessionObserver(dir, target, "C123")
	obs.initOffsets()

	// Write a JSONL file simulating a native terminal session
	sessionFile := filepath.Join(dir, "test-session.jsonl")
	f, _ := os.Create(sessionFile)
	f.WriteString(`{"type":"user","message":{"role":"user","content":"hello"},"entrypoint":"cli","sessionId":"s1"}` + "\n")
	f.WriteString(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi there"}]},"entrypoint":"cli","sessionId":"s1"}` + "\n")
	f.Close()

	ctx := context.Background()
	obs.poll(ctx)

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 2 {
		t.Fatalf("expected 2 messages, got %d: %v", len(received), received)
	}
	if !strings.HasPrefix(received[0], "user: hello") {
		t.Fatalf("unexpected first message: %s", received[0])
	}
	if !strings.Contains(received[1], "Claude: hi there") {
		t.Fatalf("unexpected second message: %s", received[1])
	}
}

type mockObserverTargetCapture struct {
	fn func(ctx context.Context, channelID, text string) error
}

func (m *mockObserverTargetCapture) SendObservation(ctx context.Context, channelID, text string) error {
	return m.fn(ctx, channelID, text)
}

func TestSessionObserverInitOffsetsSkipsExisting(t *testing.T) {
	dir := t.TempDir()

	// Write a JSONL file BEFORE creating the observer
	sessionFile := filepath.Join(dir, "existing.jsonl")
	f, _ := os.Create(sessionFile)
	f.WriteString(`{"type":"user","message":{"role":"user","content":"old message"},"entrypoint":"cli"}` + "\n")
	f.Close()

	var received []string
	var mu sync.Mutex
	target := &mockObserverTargetCapture{
		fn: func(ctx context.Context, channelID, text string) error {
			mu.Lock()
			received = append(received, text)
			mu.Unlock()
			return nil
		},
	}

	obs := newSessionObserver(dir, target, "C123")
	obs.initOffsets() // Should record current EOF

	// Poll should find nothing new
	obs.poll(context.Background())

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 0 {
		t.Fatalf("expected 0 messages (pre-existing), got %d: %v", len(received), received)
	}
}

func TestSessionObserverTruncation(t *testing.T) {
	dir := t.TempDir()

	var received []string
	var mu sync.Mutex
	target := &mockObserverTargetCapture{
		fn: func(ctx context.Context, channelID, text string) error {
			mu.Lock()
			received = append(received, text)
			mu.Unlock()
			return nil
		},
	}

	obs := newSessionObserver(dir, target, "C123")
	obs.initOffsets()

	// Write a very long message
	longText := strings.Repeat("x", 5000)
	sessionFile := filepath.Join(dir, "long.jsonl")
	f, _ := os.Create(sessionFile)
	f.WriteString(fmt.Sprintf(`{"type":"user","message":{"role":"user","content":"%s"},"entrypoint":"cli"}`, longText) + "\n")
	f.Close()

	obs.poll(context.Background())

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Fatalf("expected 1 message, got %d", len(received))
	}
	if len(received[0]) > 4000 {
		t.Fatalf("message not truncated: len=%d", len(received[0]))
	}
	if !strings.HasSuffix(received[0], "... (truncated)") {
		t.Fatal("truncated message missing suffix")
	}
}
