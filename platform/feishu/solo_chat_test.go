package feishu

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/chenhg5/cc-connect/core"
)

// soloChatFixture is a group-chat platform whose chat-info API answers with
// the given member counts (or an error code) and counts how often it is asked.
type soloChatFixture struct {
	p        *Platform
	received chan *core.Message
	chatGets *atomic.Int32
	// solo flips the chat-info answer to one person and one bot (true) or
	// two people and one bot (false) once the fixture is running.
	solo *atomic.Bool
	next int64
}

func newSoloChatFixture(t *testing.T, enabled bool, userCount, botCount string, chatCode int) *soloChatFixture {
	t.Helper()
	const appID = "cli_solo"
	const appSecret = "secret-solo"
	chatGets := &atomic.Int32{}
	solo := &atomic.Bool{}
	solo.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{
				"code": 0, "msg": "success", "expire": 7200, "tenant_access_token": "tenant-token",
			})
		case strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/chats/") && r.Method == http.MethodGet &&
			!strings.Contains(strings.TrimPrefix(r.URL.Path, "/open-apis/im/v1/chats/"), "/"):
			chatGets.Add(1)
			if chatCode != 0 {
				writeJSON(t, w, map[string]any{"code": chatCode, "msg": "no permission"})
				return
			}
			users := userCount
			if !solo.Load() {
				users = "2"
			}
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success", "data": map[string]any{
				"name": "solo", "user_count": users, "bot_count": botCount,
			}})
		default:
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success", "data": map[string]any{}})
		}
	}))
	t.Cleanup(srv.Close)

	received := make(chan *core.Message, 8)
	return &soloChatFixture{
		p: &Platform{
			platformName:     "feishu",
			domain:           srv.URL,
			appID:            appID,
			appSecret:        appSecret,
			botOpenID:        "ou_bot",
			threadIsolation:  true,
			soloChatReplyAll: enabled,
			dedup:            &core.MessageDedup{},
			client: lark.NewClient(appID, appSecret,
				lark.WithOpenBaseUrl(srv.URL),
				lark.WithHttpClient(srv.Client()),
			),
			handler: func(_ core.Platform, msg *core.Message) { received <- msg },
		},
		received: received,
		chatGets: chatGets,
		solo:     solo,
		next:     time.Now().UnixMilli(),
	}
}

// send delivers a plain text group message without any @mention.
func (f *soloChatFixture) send(t *testing.T, msgID, rootID string) {
	t.Helper()
	f.sendMentioning(t, msgID, rootID)
}

// sendMentioning delivers a plain text group message that @-mentions the
// given open ids.
func (f *soloChatFixture) sendMentioning(t *testing.T, msgID, rootID string, openIDs ...string) {
	t.Helper()
	chatType, senderType := "group", "user"
	f.next++
	createTime := strconv.FormatInt(f.next, 10)
	ev := &larkim.P2MessageReceiveV1{Event: &larkim.P2MessageReceiveV1Data{
		Sender: &larkim.EventSender{
			SenderId:   &larkim.UserId{OpenId: stringPtr("ou_user")},
			SenderType: &senderType,
		},
		Message: &larkim.EventMessage{
			MessageId:   stringPtr(msgID),
			ChatId:      stringPtr("oc_solo"),
			ChatType:    &chatType,
			MessageType: stringPtr("text"),
			Content:     stringPtr(`{"text":"帮我看看这个"}`),
			CreateTime:  &createTime,
		},
	}}
	if rootID != "" {
		ev.Event.Message.RootId = stringPtr(rootID)
	}
	for i, openID := range openIDs {
		key := "@_user_" + strconv.Itoa(i+1)
		ev.Event.Message.Mentions = append(ev.Event.Message.Mentions, &larkim.MentionEvent{
			Key: stringPtr(key), Id: &larkim.UserId{OpenId: stringPtr(openID)}, Name: stringPtr(openID),
		})
		ev.Event.Message.Content = stringPtr(`{"text":"` + key + ` 帮我看看这个"}`)
	}
	if err := f.p.onMessage(context.Background(), ev); err != nil {
		t.Fatalf("onMessage(%s) error = %v", msgID, err)
	}
}

func (f *soloChatFixture) expectDispatched(t *testing.T, msgID string) {
	t.Helper()
	select {
	case msg := <-f.received:
		if msg.MessageID != msgID {
			t.Fatalf("dispatched %q, want %q", msg.MessageID, msgID)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("%s without @mention was not dispatched", msgID)
	}
}

func (f *soloChatFixture) expectDropped(t *testing.T) {
	t.Helper()
	select {
	case msg := <-f.received:
		t.Fatalf("message without @mention should be dropped, got %q", msg.MessageID)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestSoloChat_AdmitsTopicsAndRepliesWithoutMention(t *testing.T) {
	f := newSoloChatFixture(t, true, "1", "1", 0)

	f.send(t, "om_new_topic", "")
	f.expectDispatched(t, "om_new_topic")

	f.send(t, "om_reply", "om_new_topic")
	f.expectDispatched(t, "om_reply")

	if got := f.chatGets.Load(); got != 1 {
		t.Fatalf("chat info fetched %d times, want 1 (cached)", got)
	}
}

func TestSoloChat_StillRequiresMention(t *testing.T) {
	cases := []struct {
		name               string
		enabled            bool
		userCount, botCnt  string
		chatCode           int
		wantChatInfoLookup bool
	}{
		{name: "option off", enabled: false, userCount: "1", botCnt: "1"},
		{name: "two people", enabled: true, userCount: "2", botCnt: "1", wantChatInfoLookup: true},
		{name: "two bots", enabled: true, userCount: "1", botCnt: "2", wantChatInfoLookup: true},
		{name: "chat info unreadable", enabled: true, userCount: "1", botCnt: "1", chatCode: 99991672, wantChatInfoLookup: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSoloChatFixture(t, tc.enabled, tc.userCount, tc.botCnt, tc.chatCode)
			f.send(t, "om_plain", "")
			f.expectDropped(t)
			if looked := f.chatGets.Load() > 0; looked != tc.wantChatInfoLookup {
				t.Fatalf("chat info looked up = %v, want %v", looked, tc.wantChatInfoLookup)
			}
		})
	}
}

func TestSoloChat_MentionOfSomebodyElseIsNotForTheBot(t *testing.T) {
	f := newSoloChatFixture(t, true, "1", "1", 0)

	f.sendMentioning(t, "om_for_alice", "", "ou_alice")
	f.expectDropped(t)
	if got := f.chatGets.Load(); got != 0 {
		t.Fatalf("chat info fetched %d times, want 0 (the mention already decides)", got)
	}

	f.sendMentioning(t, "om_for_both", "", "ou_alice", "ou_bot")
	f.expectDispatched(t, "om_for_both")
}

func TestSoloChat_MembershipIsReadAgainAfterTTL(t *testing.T) {
	f := newSoloChatFixture(t, true, "1", "1", 0)
	f.send(t, "om_first", "")
	f.expectDispatched(t, "om_first")

	// Somebody joined after the lookup; once the entry expires the chat is
	// read again and the mention is required.
	f.p.soloChatCache.Store("oc_solo", soloChatEntry{solo: true, fetchedAt: time.Now().Add(-soloChatCacheTTL)})
	f.solo.Store(false)
	f.send(t, "om_after_join", "")
	f.expectDropped(t)
	if got := f.chatGets.Load(); got != 2 {
		t.Fatalf("chat info fetched %d times, want 2 (expired entry re-read)", got)
	}
}

func TestSoloChat_FailedLookupIsRetried(t *testing.T) {
	f := newSoloChatFixture(t, true, "1", "1", 99991672)
	f.send(t, "om_first", "")
	f.expectDropped(t)
	f.send(t, "om_second", "")
	f.expectDropped(t)
	if got := f.chatGets.Load(); got != 2 {
		t.Fatalf("chat info fetched %d times, want 2 (failures are not cached)", got)
	}
}

func TestNewPlatform_SoloChatReplyAllOption(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		opts := map[string]any{"app_id": "cli_test", "app_secret": "secret"}
		if enabled {
			opts["solo_chat_reply_all"] = true
		}
		p, err := newPlatform("feishu", lark.FeishuBaseUrl, opts)
		if err != nil {
			t.Fatalf("newPlatform error: %v", err)
		}
		fp := extractBasePlatform(p)
		if fp.soloChatReplyAll != enabled {
			t.Fatalf("soloChatReplyAll = %v, want %v", fp.soloChatReplyAll, enabled)
		}
		if fp.groupReplyAll {
			t.Fatal("solo_chat_reply_all must not turn on group_reply_all")
		}
	}
}
