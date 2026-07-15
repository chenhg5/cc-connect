package weixin

import "testing"

func TestAuditReplyMetadata(t *testing.T) {
	meta := (&Platform{}).AuditReplyMetadata(&replyContext{
		peerUserID:   "user-1",
		contextToken: "token-1",
		messageID:    "msg-1",
		sessionKey:   "weixin:dm:user-1",
		userName:     "Alice",
	})

	if meta.SessionKey != "weixin:dm:user-1" || meta.ChannelKey != "user-1" || meta.ReplyToMessageID != "msg-1" {
		t.Fatalf("unexpected audit metadata: %+v", meta)
	}
	if meta.Extra["has_context_token"] != true {
		t.Fatalf("expected has_context_token extra, got %+v", meta.Extra)
	}
}
