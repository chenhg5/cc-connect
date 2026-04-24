package telegram

import "testing"

func TestAuditReplyMetadata(t *testing.T) {
	meta := (&Platform{}).AuditReplyMetadata(replyContext{
		chatID:     123,
		threadID:   456,
		messageID:  789,
		sessionKey: "telegram:123:456:1",
		userID:     "user-1",
		userName:   "Alice",
		chatName:   "Topic",
		channelKey: "123:456",
	})

	if meta.SessionKey != "telegram:123:456:1" || meta.ChannelKey != "123:456" || meta.ThreadID != "456" {
		t.Fatalf("unexpected audit metadata: %+v", meta)
	}
	if meta.ReplyToMessageID != "789" || meta.Extra["chat_id"] != int64(123) {
		t.Fatalf("unexpected audit extras: %+v", meta)
	}
}

func TestPreviewReceipt(t *testing.T) {
	receipt := (&Platform{}).PreviewReceipt(&telegramPreviewHandle{
		chatID:    123,
		threadID:  456,
		messageID: 789,
	})
	if receipt == nil || receipt.MessageID != "789" || receipt.ThreadID != "456" || receipt.Extra["chat_id"] != int64(123) {
		t.Fatalf("unexpected preview receipt: %+v", receipt)
	}
}
