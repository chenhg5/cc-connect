package core

import (
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// registerPureObserver registers a connection as a usage-only observer with no
// platform (a monitoring-only client that is never a reply target) and returns
// the decoded register_ack.
func registerPureObserver(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	mustWriteJSON(t, conn, map[string]any{
		"type":          "register",
		"observe_usage": true,
	})
	var ack map[string]any
	mustReadJSON(t, conn, &ack)
	return ack
}

// registerObserverAdapter registers a normal platform adapter that ALSO
// subscribes to usage events (additive) — the single-connection case where a
// client keeps full send/receive AND usage telemetry.
func registerObserverAdapter(t *testing.T, conn *websocket.Conn, platform string) map[string]any {
	t.Helper()
	mustWriteJSON(t, conn, map[string]any{
		"type":          "register",
		"platform":      platform,
		"capabilities":  []string{"text"},
		"observe_usage": true,
	})
	var ack map[string]any
	mustReadJSON(t, conn, &ack)
	return ack
}

// TestBridge_UsageObserver covers all three connection roles at the bridge
// layer (the engine side is a best-effort broadcast via BroadcastUsage,
// exercised directly here):
//   - a pure observer (no platform) is acked as observer and never becomes an
//     adapter;
//   - an adapter may additionally subscribe with observe_usage:true and keep
//     full send/receive while also receiving usage (additive);
//   - BroadcastUsage fans a usage-only event to every subscriber;
//   - the event carries token counts but NO message content;
//   - a plain platform adapter (no observe_usage) never receives it.
func TestBridge_UsageObserver(t *testing.T) {
	bs, wsURL := startTestBridge(t, "")

	// Two pure observers (no platform).
	obs1 := dialWS(t, wsURL, nil)
	if ack := registerPureObserver(t, obs1); ack["ok"] != true || ack["observer"] != true {
		t.Fatalf("pure observer1 ack = %v, want ok+observer", ack)
	}
	obs2 := dialWS(t, wsURL, nil)
	if ack := registerPureObserver(t, obs2); ack["ok"] != true || ack["observer"] != true {
		t.Fatalf("pure observer2 ack = %v, want ok+observer", ack)
	}

	// A platform adapter that ALSO subscribes to usage (single-connection case).
	hybrid := dialWS(t, wsURL, nil)
	if ack := registerObserverAdapter(t, hybrid, "hermit"); ack["ok"] != true || ack["observer"] != true {
		t.Fatalf("hybrid adapter ack = %v, want ok+observer", ack)
	}

	// A plain platform adapter — must be invisible to usage broadcasts.
	adapter := dialWS(t, wsURL, nil)
	register(t, adapter, "feishu", []string{"text"})

	// The hybrid is a real adapter; pure observers must NOT leak into adapters.
	adapters := bs.ConnectedAdapters()
	if len(adapters) != 2 {
		t.Fatalf("ConnectedAdapters = %v, want exactly [feishu hermit] (pure observers must not register as adapters)", adapters)
	}
	hasFeishu, hasHermit := false, false
	for _, a := range adapters {
		if a == "feishu" {
			hasFeishu = true
		}
		if a == "hermit" {
			hasHermit = true
		}
	}
	if !hasFeishu || !hasHermit {
		t.Fatalf("ConnectedAdapters = %v, want both feishu and hermit", adapters)
	}

	// Same BroadcastUsage call the engine makes at turn-complete.
	bs.BroadcastUsage(TurnUsage{
		SessionKey:               "feishu:chat-1:user-1",
		Platform:                 "feishu",
		AgentType:                "claudecode",
		TurnID:                   "om_turn_1",
		UserID:                   "user-1",
		UserName:                 "Alice",
		ChatName:                 "Test Group",
		InputTokens:              932,
		OutputTokens:             587,
		CacheReadInputTokens:     126016,
		CacheCreationInputTokens: 3,
	})

	jsonInt := func(m map[string]any, k string) int {
		t.Helper()
		v, ok := m[k].(float64)
		if !ok {
			t.Fatalf("missing/non-numeric %q in usage event: %v", k, m)
		}
		return int(v)
	}

	// All three subscribers (2 pure observers + the hybrid adapter) receive it.
	for i, conn := range []*websocket.Conn{obs1, obs2, hybrid} {
		msg := readMsg(t, conn)
		if msg["type"] != "usage" {
			t.Fatalf("subscriber%d: type = %v, want usage", i+1, msg["type"])
		}
		if msg["session_key"] != "feishu:chat-1:user-1" {
			t.Fatalf("subscriber%d: session_key = %v", i+1, msg["session_key"])
		}
		if msg["platform"] != "feishu" {
			t.Fatalf("subscriber%d: platform = %v", i+1, msg["platform"])
		}
		if msg["agent_type"] != "claudecode" {
			t.Fatalf("subscriber%d: agent_type = %v", i+1, msg["agent_type"])
		}
		if msg["turn_id"] != "om_turn_1" {
			t.Fatalf("subscriber%d: turn_id = %v, want om_turn_1", i+1, msg["turn_id"])
		}
		if msg["user_id"] != "user-1" {
			t.Fatalf("subscriber%d: user_id = %v, want user-1", i+1, msg["user_id"])
		}
		if msg["user_name"] != "Alice" {
			t.Fatalf("subscriber%d: user_name = %v, want Alice", i+1, msg["user_name"])
		}
		if msg["chat_name"] != "Test Group" {
			t.Fatalf("subscriber%d: chat_name = %v, want Test Group", i+1, msg["chat_name"])
		}
		if got := jsonInt(msg, "input_tokens"); got != 932 {
			t.Fatalf("subscriber%d: input_tokens = %d, want 932", i+1, got)
		}
		if got := jsonInt(msg, "output_tokens"); got != 587 {
			t.Fatalf("subscriber%d: output_tokens = %d, want 587", i+1, got)
		}
		if got := jsonInt(msg, "cache_read_input_tokens"); got != 126016 {
			t.Fatalf("subscriber%d: cache_read_input_tokens = %d, want 126016", i+1, got)
		}
		if got := jsonInt(msg, "cache_creation_input_tokens"); got != 3 {
			t.Fatalf("subscriber%d: cache_creation_input_tokens = %d, want 3", i+1, got)
		}
		if _, ok := msg["content"]; ok {
			t.Fatalf("subscriber%d: usage event must not carry message content", i+1)
		}
	}

	// The plain adapter must NOT receive the usage event.
	if err := adapter.SetReadDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	var leak map[string]any
	if err := adapter.ReadJSON(&leak); err == nil {
		t.Fatalf("plain platform adapter leaked usage event: %v", leak)
	}
}
