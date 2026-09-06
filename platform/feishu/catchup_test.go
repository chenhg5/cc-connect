package feishu

import (
	"testing"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
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
