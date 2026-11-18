package acp

import (
	"encoding/json"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestMapSessionUpdate_usageUpdateProducesNoEvent(t *testing.T) {
	params := json.RawMessage(`{
		"sessionId": "s1",
		"update": {"sessionUpdate": "usage_update", "used": 12345, "size": 262144}
	}`)
	if evs := mapSessionUpdate("s1", params); len(evs) != 0 {
		t.Fatalf("usage_update must not emit IM events, got %+v", evs)
	}
}

func TestSession_UsageUpdateCachedForGetContextUsage(t *testing.T) {
	s, _, _ := newTestSession(t, nil)

	if got := s.GetContextUsage(); got != nil {
		t.Fatalf("want nil before first usage_update, got %+v", got)
	}

	s.onNotification("session/update", json.RawMessage(`{
		"sessionId": "test-session-id",
		"update": {"sessionUpdate": "usage_update", "used": 12345, "size": 262144}
	}`))

	got := s.GetContextUsage()
	if got == nil || got.UsedTokens != 12345 || got.ContextWindow != 262144 {
		t.Fatalf("GetContextUsage = %+v", got)
	}

	// A later notification replaces the snapshot; the returned copy must be
	// a clone (mutating it must not leak into the cache).
	got.UsedTokens = -1
	s.onNotification("session/update", json.RawMessage(`{
		"sessionId": "test-session-id",
		"update": {"sessionUpdate": "usage_update", "used": 20000, "size": 262144}
	}`))
	got = s.GetContextUsage()
	if got == nil || got.UsedTokens != 20000 {
		t.Fatalf("GetContextUsage after refresh = %+v", got)
	}

	// Unrelated updates must not clobber the cached usage.
	s.onNotification("session/update", json.RawMessage(`{
		"sessionId": "test-session-id",
		"update": {"sessionUpdate": "agent_message_chunk", "content": {"type": "text", "text": "hi"}}
	}`))
	if got := s.GetContextUsage(); got == nil || got.UsedTokens != 20000 {
		t.Fatalf("usage clobbered by unrelated update: %+v", got)
	}
}

// Compile-time contract: the session satisfies the engine's usage reporter
// interface, and the agent advertises kimi acp's builtin compact command.
func TestUsageAndCompactContracts(t *testing.T) {
	var _ core.ContextUsageReporter = (*acpSession)(nil)

	compressor, ok := any(&Agent{}).(core.ContextCompressor)
	if !ok {
		t.Fatal("Agent does not implement core.ContextCompressor")
	}
	if got := compressor.CompressCommand(); got != "/compact" {
		t.Fatalf("CompressCommand = %q, want /compact", got)
	}
}
