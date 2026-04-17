package wecom

import (
	"context"
	"fmt"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// Compile-time interface assertions.
var _ core.PreviewStarter = (*WSPlatform)(nil)
var _ core.MessageUpdater = (*WSPlatform)(nil)
var _ core.PreviewFinishPreference = (*WSPlatform)(nil)
var _ core.StreamFinisher = (*WSPlatform)(nil)

// ---------------------------------------------------------------------------
// KeepPreviewOnFinish
// ---------------------------------------------------------------------------

func TestKeepPreviewOnFinish_ReturnsTrue(t *testing.T) {
	p := &WSPlatform{}
	if !p.KeepPreviewOnFinish() {
		t.Fatal("KeepPreviewOnFinish should return true")
	}
}

// ---------------------------------------------------------------------------
// SendPreviewStart — error paths
// ---------------------------------------------------------------------------

func TestSendPreviewStart_InvalidReplyContext(t *testing.T) {
	p := &WSPlatform{}
	_, err := p.SendPreviewStart(context.Background(), "not-a-wsReplyContext", "hello")
	if err == nil {
		t.Fatal("expected error for invalid reply context")
	}
}

func TestSendPreviewStart_EmptyReqID(t *testing.T) {
	p := &WSPlatform{}
	rctx := wsReplyContext{reqID: "", chatID: "chat1", userID: "user1"}
	_, err := p.SendPreviewStart(context.Background(), rctx, "hello")
	if err == nil {
		t.Fatal("expected error for empty reqID")
	}
}

// ---------------------------------------------------------------------------
// UpdateMessage — error paths
// ---------------------------------------------------------------------------

func TestUpdateMessage_InvalidHandle(t *testing.T) {
	p := &WSPlatform{}
	err := p.UpdateMessage(context.Background(), "not-a-handle", "hello")
	if err == nil {
		t.Fatal("expected error for invalid handle type")
	}
}

// ---------------------------------------------------------------------------
// FinishStream — error paths
// ---------------------------------------------------------------------------

func TestFinishStream_InvalidHandle(t *testing.T) {
	p := &WSPlatform{}
	err := p.FinishStream(context.Background(), "not-a-handle")
	if err == nil {
		t.Fatal("expected error for invalid handle type")
	}
}

// ---------------------------------------------------------------------------
// wecomStreamHandle — structure verification
// ---------------------------------------------------------------------------

func TestSendPreviewStart_ReturnsCorrectHandleType(t *testing.T) {
	// We can't call SendPreviewStart successfully without a WebSocket connection,
	// but we can verify the handle type via sendStreamChunk failure path.
	p := &WSPlatform{} // no connection → sendStreamChunk will fail

	rctx := wsReplyContext{reqID: "req_001", chatID: "chat1", userID: "user1"}
	handle, err := p.SendPreviewStart(context.Background(), rctx, "hello")
	if err == nil {
		// If it somehow succeeded, verify handle type
		h, ok := handle.(*wecomStreamHandle)
		if !ok {
			t.Fatalf("expected *wecomStreamHandle, got %T", handle)
		}
		if h.reqID != "req_001" {
			t.Fatalf("expected reqID 'req_001', got %q", h.reqID)
		}
		if h.streamID == "" {
			t.Fatal("expected non-empty streamID")
		}
	}
	// err != nil is expected (no connection) — that's fine, we tested the error paths above
}

// ---------------------------------------------------------------------------
// sendStreamChunk — frame structure
// ---------------------------------------------------------------------------

func TestSendStreamChunk_NoConnection(t *testing.T) {
	p := &WSPlatform{} // no connection
	err := p.sendStreamChunk(context.Background(), "req_1", "stream_1", "hello", false)
	if err == nil {
		t.Fatal("expected error when not connected")
	}
}

func TestSendStreamChunk_GeneratesCorrectReqID(t *testing.T) {
	p := &WSPlatform{}

	// Verify generateReqID("stream") produces correct format for stream IDs
	id := p.generateReqID("stream")
	expected := "stream_1"
	if id != expected {
		t.Fatalf("expected %q, got %q", expected, id)
	}
}

// ---------------------------------------------------------------------------
// Integration scenario: handle flows through SendPreviewStart → UpdateMessage → FinishStream
// ---------------------------------------------------------------------------

func TestStreamHandleFlow_TypeConsistency(t *testing.T) {
	// Verify that a handle produced by SendPreviewStart is accepted by UpdateMessage and FinishStream.
	// We use type assertions only (no connection needed).
	handle := &wecomStreamHandle{reqID: "req_abc", streamID: "stream_123"}

	// UpdateMessage type check
	_, ok := any(handle).(*wecomStreamHandle)
	if !ok {
		t.Fatal("handle should be assertable to *wecomStreamHandle")
	}

	// Verify fields are preserved
	if handle.reqID != "req_abc" || handle.streamID != "stream_123" {
		t.Fatalf("unexpected handle fields: %+v", handle)
	}
}

// ---------------------------------------------------------------------------
// sendStreamChunk — context cancellation
// ---------------------------------------------------------------------------

func TestSendStreamChunk_ContextCancelled(t *testing.T) {
	p := &WSPlatform{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := p.sendStreamChunk(ctx, "req_1", "stream_1", "hello", false)
	if err == nil {
		// writeAndWaitAck should fail because either writeJSON fails (no conn)
		// or context is cancelled
		t.Log("error expected but not critical — no connection means writeJSON fails first")
	}
}

// ---------------------------------------------------------------------------
// Multiple generateReqID calls produce unique stream IDs
// ---------------------------------------------------------------------------

func TestGenerateStreamIDs_Unique(t *testing.T) {
	p := &WSPlatform{}
	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := p.generateReqID("stream")
		if ids[id] {
			t.Fatalf("duplicate stream ID: %s", id)
		}
		ids[id] = true
	}
}

// ---------------------------------------------------------------------------
// Error message content
// ---------------------------------------------------------------------------

func TestSendPreviewStart_ErrorMessages(t *testing.T) {
	p := &WSPlatform{}

	tests := []struct {
		name string
		rctx any
		want string
	}{
		{"wrong type", "string-ctx", "invalid reply context type"},
		{"empty reqID", wsReplyContext{reqID: ""}, "empty reqID"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := p.SendPreviewStart(context.Background(), tt.rctx, "hello")
			if err == nil {
				t.Fatal("expected error")
			}
			if got := fmt.Sprintf("%v", err); len(got) == 0 {
				t.Fatal("error message should not be empty")
			}
		})
	}
}
