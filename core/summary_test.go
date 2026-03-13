package core

import (
	"testing"
)

func TestExtractChatKey(t *testing.T) {
	tests := []struct {
		name       string
		sessionKey string
		want       string
	}{
		{"three-part key", "feishu:chatABC:userXYZ", "feishu:chatABC"},
		{"two-part key", "feishu:chatABC", "feishu:chatABC"},
		{"single-part key", "standalone", "standalone"},
		{"empty key", "", ""},
		{"many colons", "feishu:chat:user:extra", "feishu:chat"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractChatKey(tt.sessionKey)
			if got != tt.want {
				t.Errorf("extractChatKey(%q) = %q, want %q", tt.sessionKey, got, tt.want)
			}
		})
	}
}

func TestHandleMessage_PassiveRecording(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	msg := &Message{
		SessionKey: "feishu:chatABC:user1",
		Platform:   "feishu",
		UserID:     "user1",
		UserName:   "Alice",
		Content:    "passive hello",
		Passive:    true,
		IsGroup:    true,
		ReplyCtx:   "ctx",
	}

	e.handleMessage(p, msg)

	// Should have recorded in chatLog
	entries := e.chatLog.GetRecent("feishu:chatABC", 10)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry in chatLog, got %d", len(entries))
	}
	if entries[0].Content != "passive hello" {
		t.Errorf("expected content 'passive hello', got %q", entries[0].Content)
	}
	if entries[0].UserName != "Alice" {
		t.Errorf("expected userName 'Alice', got %q", entries[0].UserName)
	}

	// Should NOT have sent any reply
	if len(p.sent) != 0 {
		t.Errorf("passive message should not trigger any reply, got %d replies", len(p.sent))
	}
}

func TestHandleMessage_ActiveGroupMessageAlsoRecorded(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	// Active (non-passive) group message should also be recorded in chatLog
	msg := &Message{
		SessionKey: "feishu:chatABC:user1",
		Platform:   "feishu",
		UserID:     "user1",
		UserName:   "Bob",
		Content:    "active group hello",
		Passive:    false,
		IsGroup:    true,
		ReplyCtx:   "ctx",
	}

	e.handleMessage(p, msg)

	entries := e.chatLog.GetRecent("feishu:chatABC", 10)
	if len(entries) != 1 {
		t.Fatalf("expected active group message to be recorded, got %d entries", len(entries))
	}
	if entries[0].Content != "active group hello" {
		t.Errorf("expected content 'active group hello', got %q", entries[0].Content)
	}
}

func TestHandleMessage_NonGroupMessageNotRecorded(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	// Non-group message (IsGroup=false) should NOT be recorded
	msg := &Message{
		SessionKey: "feishu:user1",
		Platform:   "feishu",
		UserID:     "user1",
		UserName:   "Alice",
		Content:    "private message",
		Passive:    false,
		ReplyCtx:   "ctx",
	}

	e.handleMessage(p, msg)

	entries := e.chatLog.GetRecent("feishu:user1", 10)
	if len(entries) != 0 {
		t.Errorf("private messages should not be recorded, got %d entries", len(entries))
	}
}
