package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestNotification_ToolResultPreservesNameAcrossPartialUpdates(t *testing.T) {
	s := &acpSession{ctx: context.Background(), events: make(chan core.Event, 16)}
	notify := func(update string) {
		t.Helper()
		s.onNotification("session/update", json.RawMessage(`{"sessionId":"s1","update":`+update+`}`))
	}
	check := func(eventType core.EventType, wantName string) {
		t.Helper()
		select {
		case ev := <-s.events:
			if ev.Type != eventType || ev.ToolName != wantName {
				t.Fatalf("event = %+v, want type %v and tool name %q", ev, eventType, wantName)
			}
		default:
			t.Fatal("missing tool event")
		}
	}

	notify(`{"sessionUpdate":"tool_call","toolCallId":"opaque-read","title":"read","kind":"read"}`)
	check(core.EventToolUse, "read")
	notify(`{"sessionUpdate":"tool_call","toolCallId":"opaque-shell","title":"shell","kind":"execute"}`)
	check(core.EventToolUse, "shell")
	notify(`{"sessionUpdate":"tool_call_update","toolCallId":"opaque-read","status":"in_progress","content":[{"type":"content","content":{"type":"text","text":"reading"}}]}`)
	check(core.EventToolResult, "read")
	// Titles can change independently of content and status.
	notify(`{"sessionUpdate":"tool_call_update","toolCallId":"opaque-read","title":"read README"}`)
	notify(`{"sessionUpdate":"tool_call_update","toolCallId":"opaque-shell","status":"failed","content":[{"type":"content","content":{"type":"text","text":"failed"}}]}`)
	check(core.EventToolResult, "shell")
	notify(`{"sessionUpdate":"tool_call_update","toolCallId":"opaque-read","status":"completed","content":[{"type":"content","content":{"type":"text","text":"file contents"}}]}`)
	check(core.EventToolResult, "read README")
	// A missing initial call must never expose the opaque ID as a label.
	notify(`{"sessionUpdate":"tool_call_update","toolCallId":"unknown-opaque","status":"failed"}`)
	check(core.EventToolResult, "tool")
}

func TestToolCallNames_KindFallbackSessionIsolationAndCleanup(t *testing.T) {
	s := &acpSession{acpSessID: "s1"}
	resolve := func(params, want string) {
		t.Helper()
		if got := s.resolveToolCallName(json.RawMessage(params)); got != want {
			t.Fatalf("name = %q, want %q", got, want)
		}
	}
	resolve(`{"update":{"sessionUpdate":"tool_call","toolCallId":"c1","kind":"read"}}`, "read")
	resolve(`{"sessionId":"s2","update":{"sessionUpdate":"tool_call_update","toolCallId":"c1","status":"completed"}}`, "")
	resolve(`{"update":{"sessionUpdate":"tool_call_update","toolCallId":"c1","status":"in_progress"}}`, "read")
	// Even terminal notifications without result content must discard the name.
	resolve(`{"update":{"sessionUpdate":"tool_call_update","toolCallId":"c1","status":"completed"}}`, "read")
	if len(s.toolNameByID) != 0 {
		t.Fatalf("completed call remains cached: %+v", s.toolNameByID)
	}
	resolve(`{"update":{"sessionUpdate":"tool_call_update","toolCallId":"c1","status":"failed"}}`, "")
	resolve(`{"update":{"sessionUpdate":"tool_call_update","toolCallId":"c1","title":"shell","status":"pending"}}`, "shell")
	resolve(`{"update":{"sessionUpdate":"tool_call_update","toolCallId":"c1","status":"failed"}}`, "shell")
	if len(s.toolNameByID) != 0 {
		t.Fatalf("failed call remains cached: %+v", s.toolNameByID)
	}
}

func TestToolCallNames_BoundsIncompleteCalls(t *testing.T) {
	s := &acpSession{}
	for i := 0; i <= toolNameCacheMaxEntries; i++ {
		params := json.RawMessage(fmt.Sprintf(`{"update":{"sessionUpdate":"tool_call","toolCallId":"c%d","title":"read"}}`, i))
		if got := s.resolveToolCallName(params); got != "read" {
			t.Fatalf("name = %q", got)
		}
		if len(s.toolNameByID) > toolNameCacheMaxEntries {
			t.Fatalf("cache has %d entries, limit %d", len(s.toolNameByID), toolNameCacheMaxEntries)
		}
	}
	// The newest call must survive eviction.
	params := json.RawMessage(fmt.Sprintf(`{"update":{"sessionUpdate":"tool_call_update","toolCallId":"c%d","status":"completed"}}`, toolNameCacheMaxEntries))
	if got := s.resolveToolCallName(params); got != "read" {
		t.Fatalf("newest call name = %q, want read", got)
	}
}
