package line

import "testing"

func TestAuditReplyMetadata(t *testing.T) {
	meta := (&Platform{}).AuditReplyMetadata(replyContext{
		targetID:   "group-1",
		targetType: "group",
		messageID:  "msg-1",
		sessionKey: "line:group-1",
		userID:     "user-1",
		userName:   "Alice",
		chatName:   "Team Chat",
	})

	if meta.SessionKey != "line:group-1" || meta.ChannelKey != "group-1" || meta.ReplyToMessageID != "msg-1" {
		t.Fatalf("unexpected audit metadata: %+v", meta)
	}
	if meta.Extra["target_type"] != "group" {
		t.Fatalf("expected target_type extra, got %+v", meta.Extra)
	}
}
