package weixin

import (
	"context"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// TestPickBool covers the boolean parsing helper used for the new
// dedup_enabled / dedup_window_seconds config keys (issue #1667).
func TestPickBool(t *testing.T) {
	cases := []struct {
		in  any
		want bool
	}{
		{true, true},
		{false, false},
		{1, true},
		{0, false},
		{int64(2), true},
		{float64(0.5), true},
		{float64(0), false},
		{"true", true},
		{"TRUE", true},
		{"yes", true},
		{"on", true},
		{"1", true},
		{"false", false},
		{"", false},
		{nil, false},
	}
	for _, c := range cases {
		if got := pickBool(c.in); got != c.want {
			t.Errorf("pickBool(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestNew_DedupDefaultsToEnabled guards the contract that weixin ships with
// dedup on by default (ilink can retransmit on ACK delay, see issue #1667).
func TestNew_DedupDefaultsToEnabled(t *testing.T) {
	p, err := New(map[string]any{"token": "t"})
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	plat := p.(*Platform)
	if !plat.dedupEnabled {
		t.Fatal("dedup should default to true for weixin")
	}
	if plat.dedup == nil {
		t.Fatal("dedup cache should be initialised by factory")
	}
	if plat.dedup.TTL() != 5*time.Minute {
		t.Fatalf("default TTL = %v, want 5m", plat.dedup.TTL())
	}
}

// TestNew_DedupCanBeDisabled verifies operators can opt out, e.g. for
// debugging or when they run a downstream component that already dedups.
func TestNew_DedupCanBeDisabled(t *testing.T) {
	p, err := New(map[string]any{
		"token":         "t",
		"dedup_enabled": false,
	})
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	plat := p.(*Platform)
	if plat.dedupEnabled {
		t.Fatal("dedup_enabled=false should be respected")
	}
}

// TestNew_DedupWindowRespected covers the per-deployment TTL override.
func TestNew_DedupWindowRespected(t *testing.T) {
	p, err := New(map[string]any{
		"token":                 "t",
		"dedup_window_seconds":  15,
	})
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	plat := p.(*Platform)
	if plat.dedup.TTL() != 15*time.Second {
		t.Fatalf("TTL = %v, want 15s", plat.dedup.TTL())
	}
}

// TestDispatchInbound_DropsDuplicateMessageID exercises the regression path
// from issue #1667: ilink sends the same MessageID twice; only the first
// must reach the handler.
func TestDispatchInbound_DropsDuplicateMessageID(t *testing.T) {
	p, err := New(map[string]any{
		"token":                 "t",
		"dedup_window_seconds":  60,
	})
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	plat := p.(*Platform)

	var calls int
	h := func(_ core.Platform, _ *core.Message) { calls++ }

	msg := &weixinMessage{
		MessageType:  messageTypeUser,
		FromUserID:   "u-1",
		MessageID:    7492913259736648968,
		Seq:          42,
		CreateTimeMs: time.Now().UnixMilli(),
		ClientID:     "client-a",
		ItemList:     []messageItem{{Type: messageItemText, TextItem: &textItem{Text: "hi"}}},
	}

	plat.dispatchInbound(context.Background(), msg, h)
	if calls != 1 {
		t.Fatalf("first call: handler calls = %d, want 1", calls)
	}
	// Same key — must be dropped silently.
	plat.dispatchInbound(context.Background(), msg, h)
	if calls != 1 {
		t.Fatalf("duplicate should be dropped, handler calls = %d, want 1", calls)
	}
}

// TestDispatchInbound_DedupDisabledAcceptsDuplicates confirms the toggle
// actually disables the cache (so QA can verify the regression guard).
func TestDispatchInbound_DedupDisabledAcceptsDuplicates(t *testing.T) {
	p, err := New(map[string]any{
		"token":         "t",
		"dedup_enabled": false,
	})
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	plat := p.(*Platform)

	var calls int
	h := func(_ core.Platform, _ *core.Message) { calls++ }
	msg := &weixinMessage{
		MessageType:  messageTypeUser,
		FromUserID:   "u-1",
		MessageID:    100,
		Seq:          1,
		CreateTimeMs: time.Now().UnixMilli(),
		ClientID:     "client-a",
		ItemList:     []messageItem{{Type: messageItemText, TextItem: &textItem{Text: "hi"}}},
	}
	plat.dispatchInbound(context.Background(), msg, h)
	plat.dispatchInbound(context.Background(), msg, h)
	if calls != 2 {
		t.Fatalf("dedup_enabled=false: handler calls = %d, want 2 (both should pass)", calls)
	}
}
