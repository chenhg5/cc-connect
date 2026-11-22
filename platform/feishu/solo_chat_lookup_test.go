package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// Inject the API boundary directly: these regressions never create an HTTP
// client or exchange credentials, including with a local test server.
func soloLookupResponse(t *testing.T, body string) *larkim.GetChatResp {
	t.Helper()
	resp := &larkim.GetChatResp{}
	if err := json.Unmarshal([]byte(body), resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestSoloChatLookup_DeniedAccessPreservesHistoryObservation(t *testing.T) {
	for _, tc := range []struct {
		name        string
		allowFrom   string
		allowChat   string
		wantHistory int
	}{
		{"denied user", "ou_other", "oc_main", 1},
		{"denied chat", "ou_alice", "oc_other", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newGroupHistoryTestPlatform(nil)
			p.soloChatReplyAll = true
			p.allowFrom, p.allowChat = tc.allowFrom, tc.allowChat
			calls := 0
			p.soloChatFetch = func(context.Context, string) (*larkim.GetChatResp, error) {
				calls++
				return nil, errors.New("unexpected membership lookup")
			}
			event := makeGroupHistoryEvent(t, "om_access", "ou_alice", "text", `{"text":"context"}`, "oc_main", "", false)
			if err := p.onMessage(context.Background(), event); err != nil {
				t.Fatal(err)
			}
			if calls != 0 {
				t.Fatalf("denied access made %d membership lookups", calls)
			}
			history := p.snapshotGroupHistory(p.groupHistoryScope(event.Event.Message, "oc_main"))
			if len(history.entries) != tc.wantHistory {
				t.Fatalf("history entries = %d, want %d", len(history.entries), tc.wantHistory)
			}
		})
	}
}

func TestSoloChatLookup_FailureCooldownAndRecovery(t *testing.T) {
	for _, kind := range []string{"error", "nil response", "API error", "missing data"} {
		t.Run(kind, func(t *testing.T) {
			p := &Platform{}
			calls := 0
			p.soloChatFetch = func(ctx context.Context, _ string) (*larkim.GetChatResp, error) {
				calls++
				if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 5*time.Second {
					t.Error("membership lookup must have a bounded 5s deadline")
				}
				if calls > 1 {
					return soloLookupResponse(t, `{"code":0,"data":{"user_count":"1","bot_count":"1","name":"Solo"}}`), nil
				}
				switch kind {
				case "error":
					return nil, errors.New("lookup unavailable")
				case "API error":
					return soloLookupResponse(t, `{"code":1}`), nil
				case "missing data":
					return soloLookupResponse(t, `{"code":0}`), nil
				default:
					return nil, nil
				}
			}
			first := p.isSoloChat("oc_main")
			second := p.isSoloChat("oc_main")
			if first || second || calls != 1 {
				t.Fatalf("failure must stay closed without immediate retry; calls = %d", calls)
			}
			p.soloChatCache.Store("oc_main", soloChatEntry{failed: true, fetchedAt: time.Now().Add(-soloChatFailureTTL)})
			if !p.isSoloChat("oc_main") || calls != 2 {
				t.Fatalf("expired cooldown must allow recovery; calls = %d", calls)
			}
			// Successful results still outlive the shorter failure cooldown.
			p.soloChatCache.Store("oc_main", soloChatEntry{solo: true, fetchedAt: time.Now().Add(-soloChatFailureTTL - time.Second)})
			if !p.isSoloChat("oc_main") || calls != 2 {
				t.Fatalf("success should retain the 30s TTL; calls = %d", calls)
			}
			p.soloChatCache.Store("oc_main", soloChatEntry{solo: true, fetchedAt: time.Now().Add(-soloChatCacheTTL)})
			if !p.isSoloChat("oc_main") || calls != 3 {
				t.Fatalf("expired success must refresh; calls = %d", calls)
			}
			if name, ok := p.chatNameCache.Load("oc_main"); !ok || name != "Solo" {
				t.Fatal("successful lookup should warm the chat name cache")
			}
		})
	}
}

func TestSoloChatLookup_ConcurrentUnknownFailsClosedWithoutWaiting(t *testing.T) {
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan bool, 1)
	defer func() {
		close(release)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("lookup did not finish after release")
		}
	}()
	p := &Platform{soloChatFetch: func(ctx context.Context, _ string) (*larkim.GetChatResp, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil, errors.New("lookup unavailable")
	}}
	go func() { done <- p.isSoloChat("oc_main") }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first lookup did not start")
	}
	concurrent := make(chan bool, 1)
	go func() { concurrent <- p.isSoloChat("oc_main") }()
	select {
	case solo := <-concurrent:
		if solo {
			t.Fatal("concurrent unknown membership must fail closed")
		}
	case <-time.After(time.Second):
		t.Fatal("concurrent lookup waited behind the first request")
	}
}

func TestSoloChatLookup_FreshCacheDoesNotNeedLookupSlot(t *testing.T) {
	p := &Platform{soloChatFetch: func(context.Context, string) (*larkim.GetChatResp, error) {
		t.Fatal("fresh cache must not fetch membership")
		return nil, nil
	}}
	p.soloChatCache.Store("oc_main", soloChatEntry{solo: true, fetchedAt: time.Now()})
	p.soloChatInFlight.Store("oc_main", struct{}{})
	if !p.isSoloChat("oc_main") {
		t.Fatal("fresh known membership must win over an occupied lookup slot")
	}
}

func TestSoloChatLookup_MembershipDecisionAndNegativeCache(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       bool
	}{
		{"one human one bot", `{"code":0,"data":{"user_count":"1","bot_count":"1"}}`, true},
		{"two humans", `{"code":0,"data":{"user_count":"2","bot_count":"1"}}`, false},
		{"two bots", `{"code":0,"data":{"user_count":"1","bot_count":"2"}}`, false},
		{"missing counts", `{"code":0,"data":{}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			p := &Platform{soloChatFetch: func(context.Context, string) (*larkim.GetChatResp, error) {
				calls++
				return soloLookupResponse(t, tc.body), nil
			}}
			for i := 0; i < 2; i++ {
				if got := p.isSoloChat("oc_main"); got != tc.want {
					t.Fatalf("solo=%v want=%v", got, tc.want)
				}
			}
			if calls != 1 {
				t.Fatalf("successful positive/negative result was not cached: calls=%d", calls)
			}
		})
	}
}
