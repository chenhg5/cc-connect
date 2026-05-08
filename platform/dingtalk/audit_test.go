package dingtalk

import "testing"

func TestAuditReplyMetadata(t *testing.T) {
	meta := (&Platform{}).AuditReplyMetadata(replyContext{
		conversationId: "conv-1",
		senderStaffId:  "user-1",
		messageID:      "msg-1",
		sessionKey:     "dingtalk:conv-1:user-1",
		userName:       "Alice",
		chatName:       "Ops",
	})

	if meta.SessionKey != "dingtalk:conv-1:user-1" || meta.UserID != "user-1" || meta.ChannelKey != "conv-1" {
		t.Fatalf("unexpected audit metadata: %+v", meta)
	}
	if meta.Extra["conversation_id"] != "conv-1" {
		t.Fatalf("expected conversation_id extra, got %+v", meta.Extra)
	}
}
