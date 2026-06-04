package qqbot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
	want := (1 << 25) | (1 << 26) // GROUP_AT_MESSAGE_CREATE | INTERACTION_CREATE
	if platform.intents != want {
		t.Errorf("intents = %d, want %d", platform.intents, want)
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
		_, _ = fmt.Fprint(w, "not found")
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
		_, _ = fmt.Fprint(w, "internal error")
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
			_, _ = fmt.Fprint(w, `{"access_token":"test-token","expires_in":"7200"}`)
			return
		}
		// Handle file upload request
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)
		_, _ = fmt.Fprint(w, `{"file_info":"test-file-info"}`)
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
			_, _ = fmt.Fprint(w, `{"access_token":"test-token","expires_in":"7200"}`)
			return
		}
		// Handle file upload request
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)
		_, _ = fmt.Fprint(w, `{"file_info":"test-file-info"}`)
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
		"id":           "msg-new",
		"group_openid": "group-1",
		"content":      "<@!bot123>  看看这个",
		"timestamp":    time.Now().Format(time.RFC3339),
		"message_type": msgType,
		"msg_elements": []map[string]any{
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

// ---------------------------------------------------------------------------
// SendWithButtons tests
// ---------------------------------------------------------------------------

func TestSendWithButtons_GroupMessage(t *testing.T) {
	var receivedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"id": "msg-out"}`)
	}))
	defer server.Close()

	origClient := core.HTTPClient
	t.Cleanup(func() { core.HTTPClient = origClient })
	core.HTTPClient = server.Client()

	origProd := apiBaseProduction
	apiBaseProduction = server.URL
	t.Cleanup(func() { apiBaseProduction = origProd })

	p := &Platform{
		token:       "test-token",
		tokenExpiry: time.Now().Add(time.Hour),
	}
	ctx := context.Background()
	rctx := &replyContext{
		messageType: "group",
		groupOpenID: "group-1",
		userOpenID:  "user-1",
		eventMsgID:  "evt-123",
		sessionKey:  "qqbot:group-1:user-1",
	}

	buttons := [][]core.ButtonOption{
		{
			{Text: "允许", Data: "perm:allow"},
			{Text: "拒绝", Data: "perm:deny"},
		},
		{
			{Text: "允许所有", Data: "perm:allow_all"},
		},
	}

	err := p.SendWithButtons(ctx, rctx, "权限请求", buttons)
	if err != nil {
		t.Fatalf("SendWithButtons returned error: %v", err)
	}

	if receivedBody == nil {
		t.Fatal("expected API request body")
	}

	// Check basic message fields
	if got, ok := receivedBody["content"].(string); !ok || got != "权限请求" {
		t.Errorf("content = %v, want %q", receivedBody["content"], "权限请求")
	}
	if got, ok := receivedBody["msg_type"].(float64); !ok || got != 0 {
		t.Errorf("msg_type = %v, want 0", receivedBody["msg_type"])
	}
	if got, ok := receivedBody["msg_id"].(string); !ok || got != "evt-123" {
		t.Errorf("msg_id = %v, want %q", receivedBody["msg_id"], "evt-123")
	}
	if _, ok := receivedBody["msg_seq"]; !ok {
		t.Error("msg_seq is missing")
	}

	// Check keyboard structure
	keyboard, ok := receivedBody["keyboard"].(map[string]any)
	if !ok {
		t.Fatal("keyboard is missing or not a map")
	}
	content, ok := keyboard["content"].(map[string]any)
	if !ok {
		t.Fatal("keyboard.content is missing or not a map")
	}
	rows, ok := content["rows"].([]any)
	if !ok {
		t.Fatal("keyboard.content.rows is missing or not a slice")
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}

	// Row 0: two buttons (allow, deny)
	row0, _ := rows[0].(map[string]any)
	btns0, _ := row0["buttons"].([]any)
	if len(btns0) != 2 {
		t.Fatalf("row 0: expected 2 buttons, got %d", len(btns0))
	}

	// Button 0: allow
	btn0, _ := btns0[0].(map[string]any)
	rd0, _ := btn0["render_data"].(map[string]any)
	if rd0["label"] != "允许" {
		t.Errorf("allow button label = %v, want %q", rd0["label"], "允许")
	}
	if rd0["visited_label"] != "已允许" {
		t.Errorf("allow button visited_label = %v", rd0["visited_label"])
	}
	act0, _ := btn0["action"].(map[string]any)
	if data, ok := act0["data"].(string); !ok || !strings.HasPrefix(data, "perm:allow:qqbot:group-1:user-1") {
		t.Errorf("allow button_data = %v", act0["data"])
	}
	if act0["type"] != float64(1) {
		t.Errorf("allow action type = %v, want 1", act0["type"])
	}
	if btn0["group_id"] != "perm" {
		t.Errorf("allow button group_id = %v", btn0["group_id"])
	}

	// Button 1: deny
	btn1, _ := btns0[1].(map[string]any)
	rd1, _ := btn1["render_data"].(map[string]any)
	if rd1["label"] != "拒绝" {
		t.Errorf("deny button label = %v", rd1["label"])
	}
	if rd1["visited_label"] != "已拒绝" {
		t.Errorf("deny button visited_label = %v", rd1["visited_label"])
	}
	if style, ok := rd1["style"].(float64); !ok || style != 0 {
		t.Errorf("deny button style = %v, want 0 (grey)", style)
	}
	act1, _ := btn1["action"].(map[string]any)
	if data, ok := act1["data"].(string); !ok || !strings.HasPrefix(data, "perm:deny:qqbot:group-1:user-1") {
		t.Errorf("deny button_data = %v", act1["data"])
	}

	// Row 1: allow_all button
	row1, _ := rows[1].(map[string]any)
	btns1, _ := row1["buttons"].([]any)
	if len(btns1) != 1 {
		t.Fatalf("row 1: expected 1 button, got %d", len(btns1))
	}
	btn2, _ := btns1[0].(map[string]any)
	rd2, _ := btn2["render_data"].(map[string]any)
	if rd2["label"] != "允许所有" {
		t.Errorf("allow_all button label = %v", rd2["label"])
	}
	if rd2["visited_label"] != "已始终允许" {
		t.Errorf("allow_all button visited_label = %v", rd2["visited_label"])
	}
	act2, _ := btn2["action"].(map[string]any)
	if data, ok := act2["data"].(string); !ok || !strings.HasPrefix(data, "perm:allow_all:qqbot:group-1:user-1") {
		t.Errorf("allow_all button_data = %v", act2["data"])
	}
}

func TestSendWithButtons_C2CMessage(t *testing.T) {
	var receivedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"id": "msg-out"}`)
	}))
	defer server.Close()

	origClient := core.HTTPClient
	t.Cleanup(func() { core.HTTPClient = origClient })
	core.HTTPClient = server.Client()

	origProd := apiBaseProduction
	apiBaseProduction = server.URL
	t.Cleanup(func() { apiBaseProduction = origProd })

	p := &Platform{
		token:       "test-token",
		tokenExpiry: time.Now().Add(time.Hour),
	}
	rctx := &replyContext{
		messageType: "c2c",
		userOpenID:  "user-1",
		sessionKey:  "qqbot:user-1",
	}

	buttons := [][]core.ButtonOption{
		{{Text: "允许", Data: "perm:allow"}},
	}

	err := p.SendWithButtons(context.Background(), rctx, "test", buttons)
	if err != nil {
		t.Fatalf("SendWithButtons returned error: %v", err)
	}

	// Verify C2C session key in button_data
	keyboard := receivedBody["keyboard"].(map[string]any)
	content := keyboard["content"].(map[string]any)
	rows := content["rows"].([]any)
	row0 := rows[0].(map[string]any)
	btns0 := row0["buttons"].([]any)
	btn0 := btns0[0].(map[string]any)
	act0 := btn0["action"].(map[string]any)

	if data, ok := act0["data"].(string); !ok || !strings.HasPrefix(data, "perm:allow:qqbot:user-1") {
		t.Errorf("C2C button_data = %v, want prefix perm:allow:qqbot:user-1", act0["data"])
	}
}

// TestSendWithButtons_ShareSessionInChannel verifies that when shareSessionInChannel
// is enabled, the button_data embedded session key uses the "qqbot:g:" prefix
// so it matches the session key the engine uses for shared channel sessions.
func TestSendWithButtons_ShareSessionInChannel(t *testing.T) {
	var receivedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"id": "msg-out"}`)
	}))
	defer server.Close()

	origClient := core.HTTPClient
	t.Cleanup(func() { core.HTTPClient = origClient })
	core.HTTPClient = server.Client()

	origProd := apiBaseProduction
	apiBaseProduction = server.URL
	t.Cleanup(func() { apiBaseProduction = origProd })

	p := &Platform{
		token:       "test-token",
		tokenExpiry: time.Now().Add(time.Hour),
	}
	// Simulate replyContext built with shareSessionInChannel = true
	// (sessionKey would be "qqbot:g:<groupOpenID>")
	rctx := &replyContext{
		messageType: "group",
		groupOpenID: "group-1",
		userOpenID:  "user-1",
		eventMsgID:  "evt-123",
		sessionKey:  "qqbot:g:group-1",
	}

	err := p.SendWithButtons(context.Background(), rctx, "test", [][]core.ButtonOption{
		{{Text: "允许", Data: "perm:allow"}},
	})
	if err != nil {
		t.Fatalf("SendWithButtons returned error: %v", err)
	}

	keyboard := receivedBody["keyboard"].(map[string]any)
	content := keyboard["content"].(map[string]any)
	rows := content["rows"].([]any)
	row0 := rows[0].(map[string]any)
	btns0 := row0["buttons"].([]any)
	btn0 := btns0[0].(map[string]any)
	act0 := btn0["action"].(map[string]any)

	// Must use the shared session key from replyContext, not the default group key
	if data, ok := act0["data"].(string); !ok || data != "perm:allow:qqbot:g:group-1" {
		t.Errorf("button_data = %v, want perm:allow:qqbot:g:group-1", act0["data"])
	}
}

// TestSendWithButtons_EmptySessionKey verifies that SendWithButtons returns
// an error when the replyContext has no sessionKey (e.g., constructed externally).
func TestSendWithButtons_EmptySessionKey(t *testing.T) {
	p := &Platform{
		token:       "test-token",
		tokenExpiry: time.Now().Add(time.Hour),
	}
	rctx := &replyContext{
		messageType: "group",
		groupOpenID: "group-1",
		userOpenID:  "user-1",
		// sessionKey intentionally empty
	}

	err := p.SendWithButtons(context.Background(), rctx, "test", nil)
	if err == nil {
		t.Fatal("expected error for empty sessionKey, got nil")
	}
}

// TestHandleInteractionCreate_RouterPermission is a table-driven test covering
// all permission decision routing for INTERACTION_CREATE events.
func TestHandleInteractionCreate_RouterPermission(t *testing.T) {
	tests := []struct {
		name        string
		buttonData  string
		chatType    int
		wantContent string
		wantSession string
		wantMsgType string // "group" or "c2c"
		wantCall    bool   // whether handler should be called
	}{
		{
			name:        "group allow",
			buttonData:  "perm:allow:qqbot:group-1:user-1",
			chatType:    1,
			wantContent: "allow",
			wantSession: "qqbot:group-1:user-1",
			wantMsgType: "group",
			wantCall:    true,
		},
		{
			name:        "group deny",
			buttonData:  "perm:deny:qqbot:group-2:user-2",
			chatType:    1,
			wantContent: "deny",
			wantSession: "qqbot:group-2:user-2",
			wantMsgType: "group",
			wantCall:    true,
		},
		{
			name:        "group allow_all",
			buttonData:  "perm:allow_all:qqbot:group-3:user-3",
			chatType:    1,
			wantContent: "allow all",
			wantSession: "qqbot:group-3:user-3",
			wantMsgType: "group",
			wantCall:    true,
		},
		{
			name:        "c2c allow",
			buttonData:  "perm:allow:qqbot:user-1",
			chatType:    2,
			wantContent: "allow",
			wantSession: "qqbot:user-1",
			wantMsgType: "c2c",
			wantCall:    true,
		},
		{
			name:        "c2c deny",
			buttonData:  "perm:deny:qqbot:user-2",
			chatType:    2,
			wantContent: "deny",
			wantSession: "qqbot:user-2",
			wantMsgType: "c2c",
			wantCall:    true,
		},
		{
			name:        "shared channel allow",
			buttonData:  "perm:allow:qqbot:g:group-shared",
			chatType:    1,
			wantContent: "allow",
			wantSession: "qqbot:g:group-shared",
			wantMsgType: "group",
			wantCall:    true,
		},
		{
			name:       "unknown button_data prefix",
			buttonData: "something_else:data",
			chatType:   2,
			wantCall:   false,
		},
		{
			name:       "empty decision",
			buttonData: "perm:",
			chatType:   2,
			wantCall:   false,
		},
		{
			name:       "invalid decision",
			buttonData: "perm:unknown:qqbot:user-1",
			chatType:   2,
			wantCall:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Platform{
				allowFrom: "*",
			}
			var got *core.Message
			p.handler = func(_ core.Platform, msg *core.Message) {
				got = msg
			}

			payload := map[string]any{
				"id":                  "interact-" + tt.name,
				"group_openid":        "group-1",
				"group_member_openid": "user-1",
				"user_openid":         "user-1",
				"chat_type":           tt.chatType,
				"data": map[string]any{
					"type": 11,
					"resolved": map[string]any{
						"button_data": tt.buttonData,
						"button_id":   "b_0_0",
					},
				},
			}
			data, _ := json.Marshal(payload)
			p.handleInteractionCreate(data)

			if tt.wantCall && got == nil {
				t.Fatal("expected synthetic message, got nil")
			}
			if !tt.wantCall && got != nil {
				t.Fatal("expected no message, but handler was called")
			}
			if !tt.wantCall {
				return
			}

			if got.Content != tt.wantContent {
				t.Errorf("content = %q, want %q", got.Content, tt.wantContent)
			}
			if got.SessionKey != tt.wantSession {
				t.Errorf("session_key = %q, want %q", got.SessionKey, tt.wantSession)
			}
			if got.Platform != "qqbot" {
				t.Errorf("platform = %q, want qqbot", got.Platform)
			}

			rctx, ok := got.ReplyCtx.(*replyContext)
			if !ok {
				t.Fatal("replyCtx is not *replyContext")
			}
			if rctx.messageType != tt.wantMsgType {
				t.Errorf("messageType = %q, want %q", rctx.messageType, tt.wantMsgType)
			}
			if rctx.sessionKey != tt.wantSession {
				t.Errorf("replyCtx.sessionKey = %q, want %q", rctx.sessionKey, tt.wantSession)
			}
		})
	}
}

func TestSendWithButtons_InvalidReplyCtx(t *testing.T) {
	p := &Platform{}
	err := p.SendWithButtons(context.Background(), "invalid", "test", nil)
	if err == nil {
		t.Fatal("expected error for invalid reply context, got nil")
	}
}

func TestSendWithButtons_EmptyEventMsgID(t *testing.T) {
	var receivedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"id": "msg-out"}`)
	}))
	defer server.Close()

	origClient := core.HTTPClient
	t.Cleanup(func() { core.HTTPClient = origClient })
	core.HTTPClient = server.Client()

	origProd := apiBaseProduction
	apiBaseProduction = server.URL
	t.Cleanup(func() { apiBaseProduction = origProd })

	p := &Platform{
		token:       "test-token",
		tokenExpiry: time.Now().Add(time.Hour),
	}
	rctx := &replyContext{
		messageType: "c2c",
		userOpenID:  "user-1",
		sessionKey:  "qqbot:user-1",
		// eventMsgID is empty
	}

	err := p.SendWithButtons(context.Background(), rctx, "test", [][]core.ButtonOption{{{Text: "允许", Data: "perm:allow"}}})
	if err != nil {
		t.Fatalf("SendWithButtons returned error: %v", err)
	}

	// msg_id should NOT be present when eventMsgID is empty
	if _, ok := receivedBody["msg_id"]; ok {
		t.Error("msg_id should not be present when eventMsgID is empty")
	}
	if _, ok := receivedBody["msg_seq"]; ok {
		t.Error("msg_seq should not be present when eventMsgID is empty")
	}
}

// ---------------------------------------------------------------------------
// handleInteractionCreate tests
// ---------------------------------------------------------------------------

func TestHandleInteractionCreate_Allow(t *testing.T) {
	p := &Platform{
		allowFrom: "*",
	}
	var got *core.Message
	p.handler = func(_ core.Platform, msg *core.Message) {
		got = msg
	}

	// Create an interaction event for group allow
	payload := map[string]any{
		"id":                  "interact-1",
		"group_openid":        "group-1",
		"group_member_openid": "user-1",
		"user_openid":         "user-1",
		"chat_type":           1,
		"data": map[string]any{
			"type": 11,
			"resolved": map[string]any{
				"button_data": "perm:allow:qqbot:group-1:user-1",
				"button_id":   "b_0_0",
			},
		},
	}
	data, _ := json.Marshal(payload)
	p.handleInteractionCreate(data)

	if got == nil {
		t.Fatal("expected synthetic message, got nil")
	}
	if got.Content != "allow" {
		t.Errorf("content = %q, want %q", got.Content, "allow")
	}
	if got.SessionKey != "qqbot:group-1:user-1" {
		t.Errorf("session_key = %q", got.SessionKey)
	}
	if got.MessageID != "interact-1" {
		t.Errorf("message_id = %q", got.MessageID)
	}
	if got.Platform != "qqbot" {
		t.Errorf("platform = %q", got.Platform)
	}

	// Verify reply context
	rctx, ok := got.ReplyCtx.(*replyContext)
	if !ok {
		t.Fatal("replyCtx is not *replyContext")
	}
	if rctx.messageType != "group" {
		t.Errorf("messageType = %q", rctx.messageType)
	}
	if rctx.groupOpenID != "group-1" {
		t.Errorf("groupOpenID = %q", rctx.groupOpenID)
	}
	if rctx.userOpenID != "user-1" {
		t.Errorf("userOpenID = %q", rctx.userOpenID)
	}
}

func TestHandleInteractionCreate_Deny(t *testing.T) {
	p := &Platform{
		allowFrom: "*",
	}
	var got *core.Message
	p.handler = func(_ core.Platform, msg *core.Message) {
		got = msg
	}

	payload := map[string]any{
		"id":          "interact-2",
		"user_openid": "user-1",
		"chat_type":   2,
		"data": map[string]any{
			"type": 11,
			"resolved": map[string]any{
				"button_data": "perm:deny:qqbot:user-1",
				"button_id":   "b_0_1",
			},
		},
	}
	data, _ := json.Marshal(payload)
	p.handleInteractionCreate(data)

	if got == nil {
		t.Fatal("expected synthetic message, got nil")
	}
	if got.Content != "deny" {
		t.Errorf("content = %q, want %q", got.Content, "deny")
	}
	if got.SessionKey != "qqbot:user-1" {
		t.Errorf("session_key = %q", got.SessionKey)
	}

	rctx, ok := got.ReplyCtx.(*replyContext)
	if !ok {
		t.Fatal("replyCtx is not *replyContext")
	}
	if rctx.messageType != "c2c" {
		t.Errorf("messageType = %q", rctx.messageType)
	}
	if rctx.userOpenID != "user-1" {
		t.Errorf("userOpenID = %q", rctx.userOpenID)
	}
}

func TestHandleInteractionCreate_AllowAll(t *testing.T) {
	p := &Platform{
		allowFrom: "*",
	}
	var got *core.Message
	p.handler = func(_ core.Platform, msg *core.Message) {
		got = msg
	}

	payload := map[string]any{
		"id":                  "interact-3",
		"group_openid":        "group-1",
		"group_member_openid": "user-2",
		"user_openid":         "user-2",
		"chat_type":           1,
		"data": map[string]any{
			"type": 11,
			"resolved": map[string]any{
				"button_data": "perm:allow_all:qqbot:group-1:user-2",
				"button_id":   "b_1_0",
			},
		},
	}
	data, _ := json.Marshal(payload)
	p.handleInteractionCreate(data)

	if got == nil {
		t.Fatal("expected synthetic message, got nil")
	}
	if got.Content != "allow all" {
		t.Errorf("content = %q, want %q", got.Content, "allow all")
	}
	if got.SessionKey != "qqbot:group-1:user-2" {
		t.Errorf("session_key = %q", got.SessionKey)
	}
}

func TestHandleInteractionCreate_UnknownButtonData(t *testing.T) {
	p := &Platform{
		allowFrom: "*",
	}
	called := false
	p.handler = func(_ core.Platform, msg *core.Message) {
		called = true
	}

	payload := map[string]any{
		"id":        "interact-4",
		"chat_type": 2,
		"data": map[string]any{
			"type": 11,
			"resolved": map[string]any{
				"button_data": "something_else:data",
				"button_id":   "unknown",
			},
		},
	}
	data, _ := json.Marshal(payload)
	p.handleInteractionCreate(data)

	if called {
		t.Error("handler should not be called for unknown button_data prefix")
	}
}

func TestHandleInteractionCreate_InvalidFormat(t *testing.T) {
	p := &Platform{
		allowFrom: "*",
	}
	called := false
	p.handler = func(_ core.Platform, msg *core.Message) {
		called = true
	}

	payload := map[string]any{
		"id":        "interact-5",
		"chat_type": 2,
		"data": map[string]any{
			"type": 11,
			"resolved": map[string]any{
				"button_data": "perm:",
				"button_id":   "bad",
			},
		},
	}
	data, _ := json.Marshal(payload)
	p.handleInteractionCreate(data)

	if called {
		t.Error("handler should not be called for empty button_data")
	}
}

func TestHandleInteractionCreate_EmptyID(t *testing.T) {
	p := &Platform{
		allowFrom: "*",
	}
	called := false
	p.handler = func(_ core.Platform, msg *core.Message) {
		called = true
	}

	payload := map[string]any{
		"id":        "",
		"chat_type": 2,
		"data": map[string]any{
			"resolved": map[string]any{
				"button_data": "perm:allow:qqbot:user-1",
			},
		},
	}
	data, _ := json.Marshal(payload)
	p.handleInteractionCreate(data)

	if called {
		t.Error("handler should not be called for empty interaction ID")
	}
}

func TestHandleInteractionCreate_ACKFailure(t *testing.T) {
	// Test that interaction processing proceeds even if ACK fails
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	p := &Platform{
		allowFrom:   "*",
		token:       "test-token",
		tokenExpiry: time.Now().Add(time.Hour),
	}
	// Point to test server
	origProd := apiBaseProduction
	apiBaseProduction = server.URL
	t.Cleanup(func() { apiBaseProduction = origProd })

	var got *core.Message
	p.handler = func(_ core.Platform, msg *core.Message) {
		got = msg
	}

	payload := map[string]any{
		"id":          "interact-ack",
		"user_openid": "user-1",
		"chat_type":   2,
		"data": map[string]any{
			"type": 11,
			"resolved": map[string]any{
				"button_data": "perm:allow:qqbot:user-1",
				"button_id":   "b_0_0",
			},
		},
	}
	data, _ := json.Marshal(payload)

	// Should not panic, handler should still receive the message
	p.handleInteractionCreate(data)
	if got == nil {
		t.Fatal("expected synthetic message even when ACK fails")
	}
	if got.Content != "allow" {
		t.Errorf("content = %q, want %q", got.Content, "allow")
	}
}

// ---------------------------------------------------------------------------
// ackInteraction tests
// ---------------------------------------------------------------------------

func TestAckInteraction(t *testing.T) {
	var method, reqURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		reqURL = r.URL.String()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	origClient := core.HTTPClient
	t.Cleanup(func() { core.HTTPClient = origClient })
	core.HTTPClient = server.Client()

	p := &Platform{
		token:       "test-token",
		tokenExpiry: time.Now().Add(time.Hour),
	}
	// Point to test server
	origProd := apiBaseProduction
	apiBaseProduction = server.URL
	t.Cleanup(func() { apiBaseProduction = origProd })

	err := p.ackInteraction("interact-1")
	if err != nil {
		t.Fatalf("ackInteraction returned error: %v", err)
	}
	if method != "PUT" {
		t.Errorf("method = %q, want %q", method, "PUT")
	}
	if reqURL != "/interactions/interact-1" {
		t.Errorf("url = %q, want %q", reqURL, "/interactions/interact-1")
	}
}

// ---------------------------------------------------------------------------
// Interface compliance tests
// ---------------------------------------------------------------------------

func TestPlatformImplementsInlineButtonSender(t *testing.T) {
	p := &Platform{}
	if _, ok := any(p).(core.InlineButtonSender); !ok {
		t.Error("Platform does not implement core.InlineButtonSender")
	}
}
