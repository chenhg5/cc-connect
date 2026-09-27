package qq

import "testing"

func TestAuditReplyMetadata(t *testing.T) {
	meta := (&Platform{}).AuditReplyMetadata(&replyContext{
		messageType: "group",
		userID:      123,
		groupID:     456,
		messageID:   789,
		sessionKey:  "qq:456:123",
		userName:    "Alice",
		chatName:    "Group",
	})

	if meta.SessionKey != "qq:456:123" || meta.ChannelKey != "456" || meta.ReplyToMessageID != "789" {
		t.Fatalf("unexpected audit metadata: %+v", meta)
	}
	if meta.Extra["message_type"] != "group" {
		t.Fatalf("expected message_type extra, got %+v", meta.Extra)
	}
}
