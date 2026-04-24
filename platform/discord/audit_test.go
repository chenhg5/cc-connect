package discord

import "testing"

func TestAuditReplyMetadata(t *testing.T) {
	meta := (&Platform{}).AuditReplyMetadata(replyContext{
		channelID:  "channel-1",
		messageID:  "msg-1",
		threadID:   "thread-1",
		sessionKey: "discord:channel-1:user-1",
		userID:     "user-1",
		userName:   "Alice",
		chatName:   "general",
	})

	if meta.SessionKey != "discord:channel-1:user-1" || meta.ThreadID != "thread-1" || meta.ReplyToMessageID != "msg-1" {
		t.Fatalf("unexpected message audit metadata: %+v", meta)
	}

	interactionMeta := (&Platform{}).AuditReplyMetadata(&interactionReplyCtx{
		channelID:  "channel-2",
		threadID:   "thread-2",
		sessionKey: "discord:thread-2:user-2",
		userID:     "user-2",
		userName:   "Bob",
		chatName:   "support",
	})
	if interactionMeta.ChannelKey != "channel-2" || interactionMeta.ThreadID != "thread-2" || interactionMeta.UserID != "user-2" {
		t.Fatalf("unexpected interaction audit metadata: %+v", interactionMeta)
	}
}

func TestPreviewReceipt(t *testing.T) {
	receipt := (&Platform{}).PreviewReceipt(&discordPreviewHandle{
		channelID: "channel-1",
		messageID: "msg-1",
	})
	if receipt == nil || receipt.MessageID != "msg-1" || receipt.Extra["channel_id"] != "channel-1" {
		t.Fatalf("unexpected preview receipt: %+v", receipt)
	}
}
