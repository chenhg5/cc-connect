package slack

import "testing"

func TestAuditReplyMetadata(t *testing.T) {
	meta := (&Platform{}).AuditReplyMetadata(replyContext{
		channel:    "C123",
		timestamp:  "111.222",
		threadTS:   "111.000",
		sessionKey: "slack:C123:U123",
		userID:     "U123",
		userName:   "Alice",
		chatName:   "general",
	})

	if meta.SessionKey != "slack:C123:U123" || meta.ChannelKey != "C123" || meta.ThreadID != "111.000" {
		t.Fatalf("unexpected audit metadata: %+v", meta)
	}
	if meta.Extra["channel_id"] != "C123" {
		t.Fatalf("expected channel_id extra, got %+v", meta.Extra)
	}
}
