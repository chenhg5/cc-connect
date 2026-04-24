package qqbot

import "testing"

func TestAuditReplyMetadata(t *testing.T) {
	meta := (&Platform{}).AuditReplyMetadata(&replyContext{
		messageType: "group",
		groupOpenID: "group-1",
		userOpenID:  "user-1",
		eventMsgID:  "msg-1",
		sessionKey:  "qqbot:g:group-1",
		userName:    "Alice",
		chatName:    "Group 1",
	})

	if meta.SessionKey != "qqbot:g:group-1" || meta.ChannelKey != "group-1" || meta.ReplyToMessageID != "msg-1" {
		t.Fatalf("unexpected audit metadata: %+v", meta)
	}
	if meta.Extra["message_type"] != "group" {
		t.Fatalf("expected message_type extra, got %+v", meta.Extra)
	}
}
