package weibo

import "testing"

func TestAuditReplyMetadata(t *testing.T) {
	meta := (&Platform{}).AuditReplyMetadata(replyContext{
		fromUserID: "user-1",
		sessionKey: "weibo:user-1",
		messageID:  "msg-1",
		userName:   "Alice",
		chatName:   "Alice",
	})

	if meta.SessionKey != "weibo:user-1" || meta.ChannelKey != "user-1" || meta.ReplyToMessageID != "msg-1" {
		t.Fatalf("unexpected audit metadata: %+v", meta)
	}
	if meta.Extra["to_user_id"] != "user-1" {
		t.Fatalf("expected to_user_id extra, got %+v", meta.Extra)
	}
}
