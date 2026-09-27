package wecom

import "testing"

func TestAuditReplyMetadata(t *testing.T) {
	meta := (&Platform{}).AuditReplyMetadata(replyContext{
		userID:     "user-1",
		messageID:  "msg-1",
		sessionKey: "wecom:user-1",
		userName:   "Alice",
		chatName:   "Alice",
	})

	if meta.SessionKey != "wecom:user-1" || meta.ChannelKey != "user-1" || meta.ReplyToMessageID != "msg-1" {
		t.Fatalf("unexpected http audit metadata: %+v", meta)
	}

	wsMeta := (&WSPlatform{}).AuditReplyMetadata(wsReplyContext{
		chatID:     "chat-1",
		chatType:   "group",
		userID:     "user-2",
		messageID:  "msg-2",
		sessionKey: "wecom:chat-1:user-2",
		userName:   "Bob",
		chatName:   "Ops",
	})
	if wsMeta.SessionKey != "wecom:chat-1:user-2" || wsMeta.ChannelKey != "chat-1" || wsMeta.Extra["chat_type"] != "group" {
		t.Fatalf("unexpected websocket audit metadata: %+v", wsMeta)
	}
}
