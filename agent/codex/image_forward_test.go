package codex

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsImageGenerationIntentRoutesNaturalLanguagePrompts(t *testing.T) {
	t.Parallel()

	cases := []string{
		"生成一个黄金雕像的性感女人app icon",
		"生成风格类似的logo",
		"给Tynos应用生成一个logo图片，色调#eebbcb。",
		"@AI-Infra-Andy-Local 给Tynos应用生成一张logo图片，色调#eebbcb。",
		"产图：赛博朋克猫咪头像",
		"出图 一个蓝色 app 图标",
		"create image of a golden statue app icon",
	}

	for _, tc := range cases {
		if !isImageGenerationIntent(tc, "chat-1", "user-1") {
			t.Fatalf("expected %q to route to generate-image", tc)
		}
	}
}

func TestIsImageGenerationIntentDoesNotRoutePlainDebugText(t *testing.T) {
	t.Parallel()

	cases := []string{
		"帮我看一下服务日志",
		"gh-debug https://github.com/example/repo/issues/1",
		"gh-debug https://github.com/example/repo/issues/1 生成图片的意图识别差太远了",
		"这个项目生成图片的意图识别为什么没走 cc-connect？",
		"/new",
	}

	for _, tc := range cases {
		if isImageGenerationIntent(tc, "chat-1", "user-1") {
			t.Fatalf("expected %q to stay on Codex route", tc)
		}
	}
}

func TestIsImageGenerationIntentDeskscapeLogoRequest(t *testing.T) {
	t.Parallel()

	cases := []struct {
		input string
		want  bool
	}{
		{
			input: "给 Deskscape 应用生成一个 logo 图片，这是描述 Deskscape transforms your desktop into a serene, living workspace with animated wallpapers",
			want:  true,
		},
		{
			input: "@AI-Infra-Andy-Local 给 Deskscape 应用生成一个 logo 图片，这是描述 Deskscape transforms your desktop into a serene, living workspace with animated wallpapers",
			want:  true,
		},
		{
			input: "给MyApp应用生成一个app icon",
			want:  true,
		},
		{
			input: "帮我生成一张启动页图片",
			want:  true,
		},
	}

	for _, tc := range cases {
		got := isImageGenerationIntent(tc.input, "chat-1", "user-1")
		if got != tc.want {
			t.Errorf("isImageGenerationIntent(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestTryForwardImageRequest_WithExplicitChatID(t *testing.T) {
	// Start a mock generate-image service.
	var received imageForwardPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Temporarily override the forward URL.
	origURL := generateImageForwardURL
	setGenerateImageForwardURL(server.URL)
	defer setGenerateImageForwardURL(origURL)

	// Test: explicit chatID/senderID params, empty extraEnv (no CC_SESSION_KEY).
	result := tryForwardImageRequest(
		"给 Deskscape 应用生成一个 logo 图片",
		false,
		nil,
		[]string{}, // empty extraEnv — previously this would return false
		"msg-123",
		"oc_abc123", // explicit chatID
		"ou_user1",  // explicit senderID
	)

	if !result {
		t.Fatal("tryForwardImageRequest should have returned true with explicit chatID/senderID")
	}
	if received.ChatID != "oc_abc123" {
		t.Errorf("payload.ChatID = %q, want %q", received.ChatID, "oc_abc123")
	}
	if received.SenderID != "ou_user1" {
		t.Errorf("payload.SenderID = %q, want %q", received.SenderID, "ou_user1")
	}
	if received.MessageID != "msg-123" {
		t.Errorf("payload.MessageID = %q, want %q", received.MessageID, "msg-123")
	}
}

func TestTryForwardImageRequest_WithSessionKeyInEnv(t *testing.T) {
	// Start a mock generate-image service.
	var received imageForwardPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	origURL := generateImageForwardURL
	setGenerateImageForwardURL(server.URL)
	defer setGenerateImageForwardURL(origURL)

	// Test: empty chatID/senderID params, but valid CC_SESSION_KEY in extraEnv.
	result := tryForwardImageRequest(
		"生成一个app icon",
		false,
		nil,
		[]string{"CC_SESSION_KEY=feishu:oc_chat999:ou_sender888"},
		"msg-456",
		"", // empty chatID — should fall back to extraEnv
		"", // empty senderID
	)

	if !result {
		t.Fatal("tryForwardImageRequest should have returned true with CC_SESSION_KEY in extraEnv")
	}
	if received.ChatID != "oc_chat999" {
		t.Errorf("payload.ChatID = %q, want %q", received.ChatID, "oc_chat999")
	}
	if received.SenderID != "ou_sender888" {
		t.Errorf("payload.SenderID = %q, want %q", received.SenderID, "ou_sender888")
	}
}

func TestTryForwardImageRequest_EmptyBothSources(t *testing.T) {
	// Neither explicit chatID nor CC_SESSION_KEY → should return false.
	result := tryForwardImageRequest(
		"生成一个app icon",
		false,
		nil,
		[]string{}, // no CC_SESSION_KEY
		"msg-789",
		"", // no explicit chatID
		"", // no explicit senderID
	)

	if result {
		t.Fatal("tryForwardImageRequest should return false when both chatID sources are empty")
	}
}

func TestTryForwardImageRequest_NonFeishuPlatform(t *testing.T) {
	// Non-feishu platform in CC_SESSION_KEY → should return false.
	result := tryForwardImageRequest(
		"生成一个app icon",
		false,
		nil,
		[]string{"CC_SESSION_KEY=telegram:123:456"},
		"msg-101",
		"", // no explicit chatID
		"",
	)

	if result {
		t.Fatal("tryForwardImageRequest should return false for non-feishu platforms")
	}
}

func TestTryForwardImageRequest_ExplicitParamsTakePriority(t *testing.T) {
	// Start a mock generate-image service.
	var received imageForwardPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	origURL := generateImageForwardURL
	setGenerateImageForwardURL(server.URL)
	defer setGenerateImageForwardURL(origURL)

	// Both explicit params and CC_SESSION_KEY present — explicit should win.
	result := tryForwardImageRequest(
		"生成一个app icon",
		false,
		nil,
		[]string{"CC_SESSION_KEY=feishu:oc_envChat:ou_envUser"},
		"msg-202",
		"oc_explicitChat", // explicit takes priority
		"ou_explicitUser",
	)

	if !result {
		t.Fatal("tryForwardImageRequest should have returned true")
	}
	if received.ChatID != "oc_explicitChat" {
		t.Errorf("payload.ChatID = %q, want %q (explicit should take priority)", received.ChatID, "oc_explicitChat")
	}
	if received.SenderID != "ou_explicitUser" {
		t.Errorf("payload.SenderID = %q, want %q (explicit should take priority)", received.SenderID, "ou_explicitUser")
	}
}
