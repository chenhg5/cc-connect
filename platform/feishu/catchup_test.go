package feishu

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/chenhg5/cc-connect/core"
)

func TestIsBotMentionedInList(t *testing.T) {
	const botID = "ou_bot123"

	t.Run("nil mentions", func(t *testing.T) {
		if isBotMentionedInList(nil, botID) {
			t.Fatal("expected nil mentions to not contain the bot")
		}
	})
	t.Run("empty mentions", func(t *testing.T) {
		if isBotMentionedInList([]*larkim.Mention{}, botID) {
			t.Fatal("expected empty mentions to not contain the bot")
		}
	})
	t.Run("bot not in list", func(t *testing.T) {
		mentions := []*larkim.Mention{{Id: strPtr("ou_other")}}
		if isBotMentionedInList(mentions, botID) {
			t.Fatal("expected mention list without the bot to not match")
		}
	})
	t.Run("bot in list", func(t *testing.T) {
		mentions := []*larkim.Mention{
			{Id: strPtr("ou_other")},
			{Id: strPtr(botID)},
		}
		if !isBotMentionedInList(mentions, botID) {
			t.Fatal("expected mention list containing the bot to match")
		}
	})
	t.Run("nil mention id skipped", func(t *testing.T) {
		mentions := []*larkim.Mention{{Id: nil}, {Id: strPtr(botID)}}
		if !isBotMentionedInList(mentions, botID) {
			t.Fatal("expected nil mention id to be skipped and the bot to match")
		}
	})
}

// TestCatchupMessageQualifies pins the p2p vs group qualification rules of the
// catch-up poller: p2p messages must skip the mention check entirely (the
// REST API never returns a mention list for p2p chats), while group messages
// must still require an explicit @bot mention.
func TestCatchupMessageQualifies(t *testing.T) {
	const botID = "ou_bot123"

	t.Run("p2p skips the mention check", func(t *testing.T) {
		// No mention list at all — the normal shape of a p2p message.
		if !catchupMessageQualifies(true, nil, botID) {
			t.Fatal("expected p2p message without mentions to qualify")
		}
		// Mention list mentioning someone else — still qualifies: p2p
		// messages never carry a meaningful bot mention.
		mentions := []*larkim.Mention{{Id: strPtr("ou_someone_else")}}
		if !catchupMessageQualifies(true, mentions, botID) {
			t.Fatal("expected p2p message with unrelated mentions to qualify")
		}
	})

	t.Run("group requires the bot mention", func(t *testing.T) {
		if catchupMessageQualifies(false, nil, botID) {
			t.Fatal("expected group message without mentions to be rejected")
		}
		mentions := []*larkim.Mention{{Id: strPtr("ou_other_user")}}
		if catchupMessageQualifies(false, mentions, botID) {
			t.Fatal("expected group message mentioning someone else to be rejected")
		}
		botMentions := []*larkim.Mention{
			{Id: strPtr("ou_other_user")},
			{Id: strPtr(botID)},
		}
		if !catchupMessageQualifies(false, botMentions, botID) {
			t.Fatal("expected group message mentioning the bot to qualify")
		}
	})
}

// TestCatchupSenderQualifies verifies that the poller never re-injects the
// bot's own messages, which would create a self-reply loop (the WebSocket
// push path never delivers the app's own messages).
func TestCatchupSenderQualifies(t *testing.T) {
	appSender := &larkim.Sender{SenderType: strPtr("app")}
	if catchupSenderQualifies(appSender) {
		t.Fatal("expected app-sent messages to be excluded from catchup injection")
	}

	userSender := &larkim.Sender{SenderType: strPtr("user")}
	if !catchupSenderQualifies(userSender) {
		t.Fatal("expected user-sent messages to qualify for catchup injection")
	}

	if !catchupSenderQualifies(nil) {
		t.Fatal("expected unknown sender to qualify (fail open for user messages)")
	}
}

// TestCatchupAccessAllowed pins the access-control guards of the catch-up
// injection path. Regression test for the PR #1802 review finding: the poller
// dispatches through dispatchMessage and never runs through onMessage, so
// without these guards enabling catchup_chats silently bypassed allow_from /
// allow_chat / group_only and the bot answered unauthorized users.
func TestCatchupAccessAllowed_BlocksUnauthorizedPolledMessages(t *testing.T) {
	const (
		allowedUser = "ou_allowed"
		allowedChat = "oc_allowed"
		otherUser   = "ou_intruder"
		otherChat   = "oc_other"
	)

	t.Run("user outside allow_from is rejected", func(t *testing.T) {
		if catchupAccessAllowed(allowedUser, allowedChat, false, otherUser, allowedChat, false) {
			t.Fatal("expected polled message from unauthorized user to be rejected")
		}
	})
	t.Run("user inside allow_from passes", func(t *testing.T) {
		if !catchupAccessAllowed(allowedUser, allowedChat, false, allowedUser, allowedChat, false) {
			t.Fatal("expected polled message from allowed user to pass")
		}
	})
	t.Run("group chat outside allow_chat is rejected", func(t *testing.T) {
		if catchupAccessAllowed(allowedUser, allowedChat, false, allowedUser, otherChat, false) {
			t.Fatal("expected polled message from unauthorized group chat to be rejected")
		}
	})
	t.Run("empty allow lists allow everyone", func(t *testing.T) {
		if !catchupAccessAllowed("", "", false, otherUser, otherChat, false) {
			t.Fatal("expected empty allow_from/allow_chat to allow the message (WebSocket-path parity)")
		}
	})
	t.Run("p2p message rejected when group_only is set", func(t *testing.T) {
		if catchupAccessAllowed("", "", true, otherUser, otherChat, true) {
			t.Fatal("expected p2p polled message to be rejected when group_only=true")
		}
	})
	t.Run("p2p message allowed when group_only is unset", func(t *testing.T) {
		if !catchupAccessAllowed("", "", false, otherUser, otherChat, true) {
			t.Fatal("expected p2p polled message to be allowed when group_only=false")
		}
	})
}

// TestCatchupSeenRecord is the regression test for the PR #1802 review
// finding: the shared dedup TTL (60s) is far shorter than the poll window
// (3min interval / 5min lookback), so a message listed by poll N was listed
// again — and re-injected — by poll N+1 after its dedup entry expired. The
// catch-up path must consult its own long-retention seen record, which the
// WebSocket path also writes into, so any message is considered at most once
// no matter which channel delivered it first.
func TestCatchupSeenRecord_MessageInjectedAtMostOnceAcrossPolls(t *testing.T) {
	p := &Platform{
		dedup:        &core.MessageDedup{},
		catchupDedup: core.NewMessageDedup(catchupSeenTTL),
	}
	msg := catchupTestMessage("om_dup", "user")

	// Poll 1: first sighting — the message qualifies.
	if _, ok := p.catchupShouldInject(msg, true, "ou_bot"); !ok {
		t.Fatal("expected first sighting of the message to qualify for injection")
	}
	// Poll 2, one interval later (same platform, windows overlap): the seen
	// record must reject it even though the shared dedup entry would have
	// expired by then.
	if _, ok := p.catchupShouldInject(msg, true, "ou_bot"); ok {
		t.Fatal("expected second poll over the same message to be suppressed by the catch-up seen record")
	}
}

// TestCatchupSeenRecord_WebSocketDeliveryBlocksLaterPoll pins the wiring
// between onMessage and the poller: a message the WebSocket already delivered
// (and onMessage marked in the catch-up seen record) must never be
// re-injected by a later REST poll, no matter how much later — the shared
// 60s dedup cannot cover the 3-minute poll interval.
func TestCatchupSeenRecord_WebSocketDeliveryBlocksLaterPoll(t *testing.T) {
	p := &Platform{
		dedup:        &core.MessageDedup{},
		catchupDedup: core.NewMessageDedup(catchupSeenTTL),
	}
	msg := catchupTestMessage("om_ws_first", "user")

	// The WebSocket path delivered the message first: onMessage marked it.
	p.catchupDedup.Mark("om_ws_first")

	if _, ok := p.catchupShouldInject(msg, true, "ou_bot"); ok {
		t.Fatal("expected WebSocket-delivered message to be suppressed by the catch-up seen record")
	}
}

// TestCatchupSeenTTL_CoversPollWindowOverlap pins the retention requirement:
// the seen record must outlive every future poll window a message can still
// appear in, otherwise messages re-enter a later window after their record
// expired (the double-injection bug from the PR #1802 review).
func TestCatchupSeenTTL_CoversPollWindowOverlap(t *testing.T) {
	p := &Platform{
		dedup:        &core.MessageDedup{},
		catchupDedup: core.NewMessageDedup(catchupSeenTTL),
	}
	if got := p.catchupDedup.TTL(); got < catchupLookback+catchupInterval {
		t.Fatalf("catchup seen TTL %v is shorter than lookback+interval %v", got, catchupLookback+catchupInterval)
	}
}

func TestConvertRESTMentions(t *testing.T) {
	t.Run("nil input returns empty slice", func(t *testing.T) {
		got := convertRESTMentions(nil)
		if len(got) != 0 {
			t.Fatalf("expected empty result, got %d entries", len(got))
		}
	})
	t.Run("converts open_id string to UserId struct", func(t *testing.T) {
		key, name, openID := "@_user_1", "Alice", "ou_abc"
		got := convertRESTMentions([]*larkim.Mention{{Key: &key, Id: &openID, Name: &name}})
		if len(got) != 1 {
			t.Fatalf("expected 1 mention, got %d", len(got))
		}
		if got[0].Key == nil || *got[0].Key != key {
			t.Fatalf("expected key %q, got %v", key, got[0].Key)
		}
		if got[0].Name == nil || *got[0].Name != name {
			t.Fatalf("expected name %q, got %v", name, got[0].Name)
		}
		if got[0].Id == nil || got[0].Id.OpenId == nil || *got[0].Id.OpenId != openID {
			t.Fatalf("expected open_id %q, got %v", openID, got[0].Id)
		}
	})
	t.Run("nil mention entry skipped", func(t *testing.T) {
		id := "ou_x"
		got := convertRESTMentions([]*larkim.Mention{nil, {Id: &id}})
		if len(got) != 1 {
			t.Fatalf("expected 1 mention, got %d", len(got))
		}
	})
}

// catchupTestMessage builds a minimal REST-shaped message that passes the
// freshness / sender / mention filters of catchupShouldInject. Note: the REST
// Message type carries no chat_type field (only ChatId), so none is set here.
func catchupTestMessage(id, senderID string) *larkim.Message {
	return &larkim.Message{
		MessageId:  strPtr(id),
		CreateTime: strPtr(strconv.FormatInt(time.Now().UnixMilli(), 10)),
		Sender:     &larkim.Sender{Id: strPtr(senderID), SenderType: strPtr("user")},
	}
}

// TestCatchupChatTypeMatches pins the fail-closed guard for chats listed
// under the wrong option key (PR #1802 review P2): a group chat configured
// under catchup_chats_p2p would disable the @bot mention gate and re-create
// the #1618 loud-bot failure mode, so a resolved mode contradicting the
// configured list must exclude the chat. The REST Message type carries no
// chat_type of its own, so the real mode comes from Im.Chat.Get's ChatMode
// ("group", "topic", "p2p").
func TestCatchupChatTypeMatches_FailsClosedOnWrongOptionKey(t *testing.T) {
	t.Run("group chat under p2p list is rejected", func(t *testing.T) {
		if catchupChatTypeMatches(true, "group") {
			t.Fatal("expected a group chat configured as p2p to be rejected")
		}
	})
	t.Run("p2p chat under group list is rejected", func(t *testing.T) {
		if catchupChatTypeMatches(false, "p2p") {
			t.Fatal("expected a p2p chat configured as group to be rejected")
		}
	})
	t.Run("matching types pass", func(t *testing.T) {
		if !catchupChatTypeMatches(false, "group") {
			t.Fatal("expected group/group to pass")
		}
		if !catchupChatTypeMatches(true, "p2p") {
			t.Fatal("expected p2p/p2p to pass")
		}
	})
	t.Run("topic mode counts as group", func(t *testing.T) {
		if catchupChatTypeMatches(true, "topic") {
			t.Fatal("expected a topic chat configured as p2p to be rejected")
		}
	})
	t.Run("inconclusive mode keeps configured behavior", func(t *testing.T) {
		if !catchupChatTypeMatches(true, "") {
			t.Fatal("expected unknown mode to keep the configured rule")
		}
		if !catchupChatTypeMatches(false, "unknown") {
			t.Fatal("expected unknown mode to keep the configured rule")
		}
	})
}

// TestCollectCatchupMessages pins the pagination walk of the catch-up poller
// (PR #1802 review P2): only reading the first Im.Message.List page silently
// misses messages in busy chats exactly where the compensation matters most.
// The walk must follow page tokens until the API reports no more results,
// keep API order (oldest first), and stay bounded.
func TestCollectCatchupMessages_FollowsPaginationUpToCap(t *testing.T) {
	ctx := context.Background()

	t.Run("follows page tokens until has_more is false", func(t *testing.T) {
		pages := [][]*larkim.Message{
			{catchupTestMessage("om_p1_a", "ou_u"), catchupTestMessage("om_p1_b", "ou_u")},
			{catchupTestMessage("om_p2_a", "ou_u")},
		}
		requested := 0
		fetch := func(ctx context.Context, chatID string, startTime, endTime int64, pageToken string) ([]*larkim.Message, bool, string, error) {
			requested++
			hasMore := requested < len(pages)
			next := ""
			if hasMore {
				next = fmt.Sprintf("token-%d", requested)
			}
			return pages[requested-1], hasMore, next, nil
		}
		got, err := collectCatchupMessages(ctx, fetch, "oc_chat", 1000, 2000)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if requested != 2 {
			t.Fatalf("expected 2 pages to be consumed, got %d", requested)
		}
		if len(got) != 3 {
			t.Fatalf("expected all 3 items across pages, got %d", len(got))
		}
		if got[0].MessageId == nil || *got[0].MessageId != "om_p1_a" {
			t.Fatalf("expected API order preserved, got first item %v", got[0].MessageId)
		}
	})

	t.Run("caps the walk at catchupMaxPages", func(t *testing.T) {
		calls := 0
		fetch := func(ctx context.Context, chatID string, startTime, endTime int64, pageToken string) ([]*larkim.Message, bool, string, error) {
			calls++
			return []*larkim.Message{catchupTestMessage(fmt.Sprintf("om_%d", calls), "ou_u")}, true, fmt.Sprintf("token-%d", calls), nil
		}
		got, err := collectCatchupMessages(ctx, fetch, "oc_chat", 1000, 2000)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if calls != catchupMaxPages {
			t.Fatalf("expected the walk to stop at %d pages, consumed %d", catchupMaxPages, calls)
		}
		if len(got) != catchupMaxPages {
			t.Fatalf("expected %d items, got %d", catchupMaxPages, len(got))
		}
	})

	t.Run("propagates fetch errors", func(t *testing.T) {
		fetch := func(ctx context.Context, chatID string, startTime, endTime int64, pageToken string) ([]*larkim.Message, bool, string, error) {
			return nil, false, "", errors.New("boom")
		}
		if _, err := collectCatchupMessages(ctx, fetch, "oc_chat", 1000, 2000); err == nil {
			t.Fatal("expected the fetch error to propagate")
		}
	})
}
