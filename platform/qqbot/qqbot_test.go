package qqbot

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

func TestDownloadAttachmentImages_ChecksStatusCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, "not found")
	}))
	defer server.Close()

	origClient := core.HTTPClient
	t.Cleanup(func() { core.HTTPClient = origClient })
	core.HTTPClient = server.Client()

	attachments := []attachment{
		{ContentType: "image/png", URL: server.URL + "/image.png"},
	}
	images := downloadAttachmentImages(attachments)
	if len(images) != 0 {
		t.Fatalf("expected 0 images on non-200 status, got %d", len(images))
	}
}

func TestDownloadAttachmentImages_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("fake-png-data"))
	}))
	defer server.Close()

	origClient := core.HTTPClient
	t.Cleanup(func() { core.HTTPClient = origClient })
	core.HTTPClient = server.Client()

	attachments := []attachment{
		{ContentType: "image/png", URL: server.URL + "/image.png", Filename: "test.png"},
	}
	images := downloadAttachmentImages(attachments)
	if len(images) != 1 {
		t.Fatalf("expected 1 image, got %d", len(images))
	}
	if string(images[0].Data) != "fake-png-data" {
		t.Fatalf("image data = %q, want %q", string(images[0].Data), "fake-png-data")
	}
	if images[0].FileName != "test.png" {
		t.Fatalf("filename = %q, want %q", images[0].FileName, "test.png")
	}
}

func TestDownloadAttachmentFiles_ChecksStatusCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, "internal error")
	}))
	defer server.Close()

	origClient := core.HTTPClient
	t.Cleanup(func() { core.HTTPClient = origClient })
	core.HTTPClient = server.Client()

	attachments := []attachment{
		{ContentType: "application/pdf", URL: server.URL + "/file.pdf"},
	}
	files := downloadAttachmentFiles(attachments)
	if len(files) != 0 {
		t.Fatalf("expected 0 files on non-200 status, got %d", len(files))
	}
}

func TestDownloadAttachmentFiles_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.Write([]byte("fake-pdf-data"))
	}))
	defer server.Close()

	origClient := core.HTTPClient
	t.Cleanup(func() { core.HTTPClient = origClient })
	core.HTTPClient = server.Client()

	attachments := []attachment{
		{ContentType: "application/pdf", URL: server.URL + "/file.pdf", Filename: "doc.pdf"},
	}
	files := downloadAttachmentFiles(attachments)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if string(files[0].Data) != "fake-pdf-data" {
		t.Fatalf("file data = %q, want %q", string(files[0].Data), "fake-pdf-data")
	}
	if files[0].FileName != "doc.pdf" {
		t.Fatalf("filename = %q, want %q", files[0].FileName, "doc.pdf")
	}
}

func TestDownloadAttachmentFiles_SkipsImages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("data"))
	}))
	defer server.Close()

	origClient := core.HTTPClient
	t.Cleanup(func() { core.HTTPClient = origClient })
	core.HTTPClient = server.Client()

	attachments := []attachment{
		{ContentType: "image/png", URL: server.URL + "/image.png"},
		{ContentType: "application/pdf", URL: server.URL + "/file.pdf"},
	}
	// Verify that downloadAttachmentFiles skips image content types
	files := downloadAttachmentFiles(attachments)
	for _, f := range files {
		if f.MimeType == "image/png" {
			t.Fatal("expected no image files in downloadAttachmentFiles result")
		}
	}
}

func TestDownloadAttachmentFiles_SkipsEmptyURL(t *testing.T) {
	attachments := []attachment{
		{ContentType: "application/pdf", URL: ""},
	}
	files := downloadAttachmentFiles(attachments)
	if len(files) != 0 {
		t.Fatalf("expected 0 files for empty URL, got %d", len(files))
	}
}

func TestUploadRichMedia_IncludesFileNameForFileType4(t *testing.T) {
	var receivedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Handle token request
		if r.URL.Path == "/app/getAppAccessToken" {
			fmt.Fprint(w, `{"access_token":"test-token","expires_in":"7200"}`)
			return
		}
		// Handle file upload request
		json.NewDecoder(r.Body).Decode(&receivedBody)
		fmt.Fprint(w, `{"file_info":"test-file-info"}`)
	}))
	defer server.Close()

	origClient := core.HTTPClient
	t.Cleanup(func() { core.HTTPClient = origClient })
	core.HTTPClient = server.Client()

	origTokenURL := tokenURL
	origApiBaseProduction := apiBaseProduction
	tokenURL = server.URL + "/app/getAppAccessToken"
	apiBaseProduction = server.URL
	t.Cleanup(func() {
		tokenURL = origTokenURL
		apiBaseProduction = origApiBaseProduction
	})

	p := &Platform{
		sandbox:     false,
		token:       "test-token",
		tokenExpiry: time.Now().Add(time.Hour),
	}
	rctx := &replyContext{
		messageType: "c2c",
		userOpenID:  "user-123",
	}

	fileInfo, err := p.uploadRichMedia(rctx, 4, []byte("file-data"), "document.pdf")
	if err != nil {
		t.Fatalf("uploadRichMedia returned error: %v", err)
	}
	if fileInfo != "test-file-info" {
		t.Fatalf("fileInfo = %q, want %q", fileInfo, "test-file-info")
	}
	if receivedBody["file_name"] != "document.pdf" {
		t.Fatalf("file_name = %v, want %q", receivedBody["file_name"], "document.pdf")
	}
	if receivedBody["file_type"].(float64) != 4 {
		t.Fatalf("file_type = %v, want 4", receivedBody["file_type"])
	}
}

func TestUploadRichMedia_NoFileNameForOtherFileTypes(t *testing.T) {
	var receivedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Handle token request
		if r.URL.Path == "/app/getAppAccessToken" {
			fmt.Fprint(w, `{"access_token":"test-token","expires_in":"7200"}`)
			return
		}
		// Handle file upload request
		json.NewDecoder(r.Body).Decode(&receivedBody)
		fmt.Fprint(w, `{"file_info":"test-file-info"}`)
	}))
	defer server.Close()

	origClient := core.HTTPClient
	t.Cleanup(func() { core.HTTPClient = origClient })
	core.HTTPClient = server.Client()

	origTokenURL := tokenURL
	origApiBaseProduction := apiBaseProduction
	tokenURL = server.URL + "/app/getAppAccessToken"
	apiBaseProduction = server.URL
	t.Cleanup(func() {
		tokenURL = origTokenURL
		apiBaseProduction = origApiBaseProduction
	})

	p := &Platform{
		sandbox:     false,
		token:       "test-token",
		tokenExpiry: time.Now().Add(time.Hour),
	}
	rctx := &replyContext{
		messageType: "c2c",
		userOpenID:  "user-123",
	}

	// fileType 1 (image) should NOT include file_name
	fileInfo, err := p.uploadRichMedia(rctx, 1, []byte("image-data"), "")
	if err != nil {
		t.Fatalf("uploadRichMedia returned error: %v", err)
	}
	if fileInfo != "test-file-info" {
		t.Fatalf("fileInfo = %q, want %q", fileInfo, "test-file-info")
	}
	if _, hasFileName := receivedBody["file_name"]; hasFileName {
		t.Fatalf("expected no file_name for fileType 1, got %v", receivedBody["file_name"])
	}
}

func TestQuotedTextFromElements(t *testing.T) {
	tests := []struct {
		name     string
		elements []msgElement
		want     string
	}{
		{
			name:     "empty elements",
			elements: nil,
			want:     "",
		},
		{
			name:     "element with content",
			elements: []msgElement{{Content: "被引用的消息"}},
			want:     "被引用的消息",
		},
		{
			name:     "element with whitespace content",
			elements: []msgElement{{Content: "  "}},
			want:     "",
		},
		{
			name: "element with only attachments",
			elements: []msgElement{
				{Attachments: []attachment{{ContentType: "image/png", URL: "https://example.com/img.png"}}},
			},
			want: "[图片]",
		},
		{
			name: "content takes priority over attachments",
			elements: []msgElement{
				{
					Content:     "有内容的消息",
					Attachments: []attachment{{ContentType: "image/png", URL: "https://example.com/img.png"}},
				},
			},
			want: "有内容的消息",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := quotedTextFromElements(tt.elements)
			if got != tt.want {
				t.Errorf("quotedTextFromElements() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHandleC2CMessage_QuoteFromMsgElements(t *testing.T) {
	p := &Platform{
		allowFrom:    "*",
		messageCache: map[string]cachedMessage{},
	}

	var got *core.Message
	p.handler = func(_ core.Platform, msg *core.Message) {
		got = msg
	}

	// Simulate a quote message (message_type=103) with msg_elements[0] containing the quoted content
	msgType := 103
	payload := map[string]any{
		"id":        "msg-new",
		"content":   "我的回复",
		"timestamp": time.Now().Format(time.RFC3339),
		"author": map[string]any{
			"user_openid": "user-1",
		},
		"message_type": msgType,
		"msg_elements": []map[string]any{
			{"content": "这是被引用的消息内容"},
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
	want := "[引用消息]\n这是被引用的消息内容\n\n我的回复"
	if got.Content != want {
		t.Fatalf("content = %q, want %q", got.Content, want)
	}
}

func TestHandleGroupMessage_QuoteFromMsgElements(t *testing.T) {
	p := &Platform{
		allowFrom:    "*",
		messageCache: map[string]cachedMessage{},
	}

	var got *core.Message
	p.handler = func(_ core.Platform, msg *core.Message) {
		got = msg
	}

	// Simulate a group quote message (message_type=103) with msg_elements[0]
	msgType := 103
	payload := map[string]any{
		"id":             "msg-new",
		"group_openid":   "group-1",
		"content":        "<@!bot123>  看看这个",
		"timestamp":      time.Now().Format(time.RFC3339),
		"message_type":   msgType,
		"msg_elements":   []map[string]any{
			{"content": "之前的讨论内容"},
		},
		"author": map[string]any{
			"member_openid": "user-1",
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	p.handleGroupMessage(data)

	if got == nil {
		t.Fatal("expected message")
	}
	want := "[引用消息]\n之前的讨论内容\n\n看看这个"
	if got.Content != want {
		t.Fatalf("content = %q, want %q", got.Content, want)
	}
}
