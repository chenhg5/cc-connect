package qqbot

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestPlatform_Name(t *testing.T) {
	p := &Platform{}
	if got := p.Name(); got != "qqbot" {
		t.Errorf("Name() = %q, want %q", got, "qqbot")
	}
}

func TestNew_MissingAppID(t *testing.T) {
	_, err := New(map[string]any{
		"app_secret": "test-secret",
	})
	if err == nil {
		t.Error("expected error for missing app_id, got nil")
	}
}

func TestNew_MissingAppSecret(t *testing.T) {
	_, err := New(map[string]any{
		"app_id": "test-app-id",
	})
	if err == nil {
		t.Error("expected error for missing app_secret, got nil")
	}
}

func TestNew_MissingBoth(t *testing.T) {
	_, err := New(map[string]any{})
	if err == nil {
		t.Error("expected error for missing credentials, got nil")
	}
}

func TestNew_WithValidCredentials(t *testing.T) {
	p, err := New(map[string]any{
		"app_id":     "test-app-id",
		"app_secret": "test-secret",
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected platform, got nil")
	}
	if p.Name() != "qqbot" {
		t.Errorf("Name() = %q, want %q", p.Name(), "qqbot")
	}
}

func TestNew_Sandbox(t *testing.T) {
	p, err := New(map[string]any{
		"app_id":     "test-app-id",
		"app_secret": "test-secret",
		"sandbox":    true,
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	platform := p.(*Platform)
	if !platform.sandbox {
		t.Error("sandbox = false, want true")
	}
}

func TestNew_DefaultIntents(t *testing.T) {
	p, err := New(map[string]any{
		"app_id":     "test-app-id",
		"app_secret": "test-secret",
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	platform := p.(*Platform)
	if platform.intents != defaultIntents {
		t.Errorf("intents = %d, want %d (defaultIntents)", platform.intents, defaultIntents)
	}
}

func TestNew_CustomIntents(t *testing.T) {
	p, err := New(map[string]any{
		"app_id":     "test-app-id",
		"app_secret": "test-secret",
		"intents":    1 << 20,
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	platform := p.(*Platform)
	if platform.intents != 1<<20 {
		t.Errorf("intents = %d, want %d", platform.intents, 1<<20)
	}
}

func TestNew_IntentsAsFloat(t *testing.T) {
	p, err := New(map[string]any{
		"app_id":     "test-app-id",
		"app_secret": "test-secret",
		"intents":    float64(1 << 18),
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	platform := p.(*Platform)
	if platform.intents != 1<<18 {
		t.Errorf("intents = %d, want %d", platform.intents, 1<<18)
	}
}

func TestNew_WithAllowFrom(t *testing.T) {
	p, err := New(map[string]any{
		"app_id":     "test-app-id",
		"app_secret": "test-secret",
		"allow_from": "user1,user2",
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	platform := p.(*Platform)
	if platform.allowFrom != "user1,user2" {
		t.Errorf("allowFrom = %q, want %q", platform.allowFrom, "user1,user2")
	}
}

func TestNew_ShareSessionInChannel(t *testing.T) {
	p, err := New(map[string]any{
		"app_id":                   "test-app-id",
		"app_secret":               "test-secret",
		"share_session_in_channel": true,
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	platform := p.(*Platform)
	if !platform.shareSessionInChannel {
		t.Error("shareSessionInChannel = false, want true")
	}
}

func TestNew_MarkdownSupport(t *testing.T) {
	p, err := New(map[string]any{
		"app_id":           "test-app-id",
		"app_secret":       "test-secret",
		"markdown_support": true,
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	platform := p.(*Platform)
	if !platform.markdownSupport {
		t.Error("markdownSupport = false, want true")
	}
}

func TestPrependQuotedMessage(t *testing.T) {
	got := prependQuotedMessage("上一条内容", "现在这条")
	want := "[引用消息]\n上一条内容\n\n现在这条"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestResolveQuotedText_FromCache(t *testing.T) {
	p := &Platform{
		messageCache: map[string]cachedMessage{
			"msg-1": {Content: "缓存里的原文", UpdatedAt: time.Now()},
		},
	}
	got := p.resolveQuotedText(&messageReference{MessageID: "msg-1"})
	if got != "缓存里的原文" {
		t.Fatalf("got %q", got)
	}
}

func TestHandleC2CMessage_WithMessageReference(t *testing.T) {
	p := &Platform{
		allowFrom: "*",
		messageCache: map[string]cachedMessage{
			"msg-ref": {Content: "被引用的那条", UpdatedAt: time.Now()},
		},
	}

	var got *core.Message
	p.handler = func(_ core.Platform, msg *core.Message) {
		got = msg
	}

	payload := map[string]any{
		"id":        "msg-new",
		"content":   "现在这条",
		"timestamp": time.Now().Format(time.RFC3339),
		"author": map[string]any{
			"user_openid": "user-1",
		},
		"message_reference": map[string]any{
			"message_id": "msg-ref",
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	p.handleC2CMessage(data)

	if got == nil {
		t.Fatal("expected message")
	}
	want := "[引用消息]\n被引用的那条\n\n现在这条"
	if got.Content != want {
		t.Fatalf("content = %q want %q", got.Content, want)
	}
	if cached := p.messageCache["msg-new"].Content; cached != want {
		t.Fatalf("cached content = %q want %q", cached, want)
	}
}

// verify Platform implements core.Platform
var _ core.Platform = (*Platform)(nil)
