package feishu

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestOnMessageRecalledDispatchesCoreRecallMessage(t *testing.T) {
	got := make(chan *core.Message, 1)
	p := &Platform{
		platformName: "feishu",
		handler: func(_ core.Platform, msg *core.Message) {
			got <- msg
		},
	}
	messageID := "om_recalled"
	chatID := "oc_chat"
	recallTime := "1710000000000"
	recallType := "user"

	err := p.onMessageRecalled(context.Background(), &larkim.P2MessageRecalledV1{
		Event: &larkim.P2MessageRecalledV1Data{
			MessageId:  &messageID,
			ChatId:     &chatID,
			RecallTime: &recallTime,
			RecallType: &recallType,
		},
	})
	if err != nil {
		t.Fatalf("onMessageRecalled returned error: %v", err)
	}

	select {
	case msg := <-got:
		if msg.Platform != "feishu" {
			t.Fatalf("Platform = %q, want feishu", msg.Platform)
		}
		if msg.MessageID != messageID {
			t.Fatalf("MessageID = %q, want %q", msg.MessageID, messageID)
		}
		if !msg.Recalled {
			t.Fatal("Recalled = false, want true")
		}
		if msg.Content != "" {
			t.Fatalf("Content = %q, want empty recall payload", msg.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for recall message")
	}
}

func TestDispatchMessageDropsRecalledMessageBeforeHandler(t *testing.T) {
	called := false
	p := &Platform{
		platformName: "feishu",
		handler: func(_ core.Platform, _ *core.Message) {
			called = true
		},
	}
	p.markMessageRecalled("om_drop")

	p.dispatchMessage(
		context.Background(),
		"text",
		`{"text":"hello"}`,
		nil,
		"om_drop",
		"feishu:ou_user:ou_user",
		"",
		"",
		replyContext{messageID: "om_drop", sessionKey: "feishu:ou_user:ou_user"},
		"",
		0,
	)

	if called {
		t.Fatal("handler was called for a message already marked recalled")
	}
}

func TestDispatchMessageIncludesQuotedImage(t *testing.T) {
	const appID = "cli_quote_image"
	const appSecret = "secret-quote-image"
	const parentMessageID = "om_parent_image"
	const imageKey = "img_parent"

	imageData := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

	tests := []struct {
		name    string
		msgType string
		content string
	}{
		{
			name:    "text reply",
			msgType: "text",
			content: `{"text":"这是什么图"}`,
		},
		{
			name:    "post reply",
			msgType: "post",
			content: `{"content":[[{"tag":"text","text":"这是什么图"}]]}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := make(chan *core.Message, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
					w.Header().Set("Content-Type", "application/json")
					writeJSON(t, w, map[string]any{
						"code":                0,
						"msg":                 "success",
						"expire":              7200,
						"tenant_access_token": "tenant-token",
					})
				case r.URL.Path == "/open-apis/im/v1/messages/"+parentMessageID:
					w.Header().Set("Content-Type", "application/json")
					writeJSON(t, w, map[string]any{
						"code": 0,
						"msg":  "success",
						"data": map[string]any{
							"items": []map[string]any{
								{
									"msg_type":  "image",
									"parent_id": "",
									"sender": map[string]any{
										"id":          "",
										"sender_type": "user",
									},
									"body": map[string]any{
										"content": `{"image_key":"` + imageKey + `"}`,
									},
								},
							},
						},
					})
				case r.URL.Path == "/open-apis/im/v1/messages/"+parentMessageID+"/resources/"+imageKey:
					if r.URL.Query().Get("type") != "image" {
						t.Fatalf("resource type = %q, want image", r.URL.Query().Get("type"))
					}
					w.Header().Set("Content-Type", "image/png")
					if _, err := w.Write(imageData); err != nil {
						t.Fatalf("write image: %v", err)
					}
				default:
					t.Fatalf("unexpected path %s", r.URL.Path)
				}
			}))
			defer srv.Close()

			p := &Platform{
				platformName: "feishu",
				domain:       srv.URL,
				appID:        appID,
				appSecret:    appSecret,
				client: lark.NewClient(appID, appSecret,
					lark.WithOpenBaseUrl(srv.URL),
					lark.WithHttpClient(srv.Client()),
				),
				handler: func(_ core.Platform, msg *core.Message) {
					got <- msg
				},
			}

			p.dispatchMessage(
				context.Background(),
				tc.msgType,
				tc.content,
				nil,
				"om_child",
				"feishu:oc_chat:ou_user",
				"",
				"",
				replyContext{messageID: "om_child", sessionKey: "feishu:oc_chat:ou_user"},
				parentMessageID,
				0,
			)

			select {
			case msg := <-got:
				if msg.Content != "这是什么图" {
					t.Fatalf("Content = %q, want question text", msg.Content)
				}
				if !strings.Contains(msg.ExtraContent, "[image]") {
					t.Fatalf("ExtraContent = %q, want quoted image marker", msg.ExtraContent)
				}
				if len(msg.Images) != 1 {
					t.Fatalf("len(Images) = %d, want 1", len(msg.Images))
				}
				if string(msg.Images[0].Data) != string(imageData) {
					t.Fatal("quoted image data did not match downloaded resource")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for dispatched message")
			}
		})
	}
}

func TestDispatchMessageKeepsMentionOnlyQuotedText(t *testing.T) {
	const appID = "cli_quote_text"
	const appSecret = "secret-quote-text"
	const parentMessageID = "om_parent_text"

	got := make(chan *core.Message, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			w.Header().Set("Content-Type", "application/json")
			writeJSON(t, w, map[string]any{
				"code":                0,
				"msg":                 "success",
				"expire":              7200,
				"tenant_access_token": "tenant-token",
			})
		case r.URL.Path == "/open-apis/im/v1/messages/"+parentMessageID:
			w.Header().Set("Content-Type", "application/json")
			writeJSON(t, w, map[string]any{
				"code": 0,
				"msg":  "success",
				"data": map[string]any{
					"items": []map[string]any{
						{
							"msg_type":  "text",
							"parent_id": "",
							"sender": map[string]any{
								"id":          "ou_parent",
								"sender_type": "user",
							},
							"body": map[string]any{
								"content": `{"text":"请总结这条消息"}`,
							},
						},
					},
				},
			})
		case strings.HasPrefix(r.URL.Path, "/open-apis/contact/v3/users/"):
			w.Header().Set("Content-Type", "application/json")
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success"})
		case strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/chats/"):
			w.Header().Set("Content-Type", "application/json")
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	p := &Platform{
		platformName: "feishu",
		domain:       srv.URL,
		appID:        appID,
		appSecret:    appSecret,
		botOpenID:    "ou_bot",
		client: lark.NewClient(appID, appSecret,
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
		handler: func(_ core.Platform, msg *core.Message) {
			got <- msg
		},
	}

	p.dispatchMessage(
		context.Background(),
		"text",
		`{"text":"@bot"}`,
		[]*larkim.MentionEvent{
			{Key: strPtr("@bot"), Id: &larkim.UserId{OpenId: strPtr("ou_bot")}, Name: strPtr("Bot")},
		},
		"om_child",
		"feishu:oc_chat:ou_user",
		"ou_user",
		"oc_chat",
		replyContext{messageID: "om_child", sessionKey: "feishu:oc_chat:ou_user"},
		parentMessageID,
		0,
	)

	select {
	case msg := <-got:
		if msg.Content != "" {
			t.Fatalf("Content = %q, want empty user text after bot mention stripping", msg.Content)
		}
		if !strings.Contains(msg.ExtraContent, "请总结这条消息") {
			t.Fatalf("ExtraContent = %q, want quoted text", msg.ExtraContent)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for mention-only quoted text message")
	}
}

func TestOnMessageRepliesToUnauthorizedMention(t *testing.T) {
	const appID = "cli_unauthorized"
	const appSecret = "secret-unauthorized"
	const botOpenID = "ou_bot"
	const userOpenID = "ou_blocked"

	replyBodies := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{
				"code":                0,
				"msg":                 "success",
				"expire":              7200,
				"tenant_access_token": "tenant-token",
			})
		case strings.HasSuffix(r.URL.Path, "/reply"):
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read reply body: %v", err)
			}
			replyBodies <- string(body)
			writeJSON(t, w, map[string]any{
				"code": 0,
				"msg":  "success",
				"data": map[string]any{"message_id": "om_reply_ok"},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	p := &Platform{
		platformName: "feishu",
		domain:       srv.URL,
		appID:        appID,
		appSecret:    appSecret,
		allowFrom:    "ou_allowed",
		botOpenID:    botOpenID,
		dedup:        &core.MessageDedup{},
		client: lark.NewClient(appID, appSecret,
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
		handler: func(core.Platform, *core.Message) {
			t.Fatal("handler should not run for unauthorized sender")
		},
	}

	chatType := "group"
	msgType := "text"
	senderType := "user"
	content := `{"text":"@_user_1 hello"}`
	createTime := strconv.FormatInt(time.Now().UnixMilli(), 10)
	err := p.onMessage(context.Background(), &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderId:   &larkim.UserId{OpenId: stringPtr(userOpenID)},
				SenderType: &senderType,
			},
			Message: &larkim.EventMessage{
				MessageId:   stringPtr("om_unauthorized"),
				ChatId:      stringPtr("oc_group"),
				ChatType:    &chatType,
				MessageType: &msgType,
				Content:     &content,
				CreateTime:  &createTime,
				Mentions: []*larkim.MentionEvent{
					{
						Key:  stringPtr("@_user_1"),
						Id:   &larkim.UserId{OpenId: stringPtr(botOpenID)},
						Name: stringPtr("bot"),
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}

	select {
	case got := <-replyBodies:
		if !strings.Contains(got, core.UnauthorizedAccessMessage) {
			t.Fatalf("reply body = %q, want unauthorized message", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for unauthorized reply")
	}
}

func TestIsMessageRecalledDetectsWithdrawnMessageFromGetAPI(t *testing.T) {
	const appID = "cli_recall_probe"
	const appSecret = "secret-recall-probe"

	getCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{
				"code":                0,
				"msg":                 "success",
				"expire":              7200,
				"tenant_access_token": "tenant-token",
			})
		case "/open-apis/im/v1/messages/om_withdrawn":
			getCalls++
			writeJSON(t, w, map[string]any{
				"code": 230011,
				"msg":  "The message was withdrawn.",
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	p := &Platform{
		platformName: "feishu",
		domain:       srv.URL,
		appID:        appID,
		appSecret:    appSecret,
		client: lark.NewClient(appID, appSecret,
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
		replayClient: lark.NewClient(appID, appSecret,
			lark.WithEnableTokenCache(false),
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
	}

	recalled, err := p.IsMessageRecalled(context.Background(), replyContext{messageID: "om_withdrawn", chatID: "oc_chat"})
	if err != nil {
		t.Fatalf("IsMessageRecalled() error = %v", err)
	}
	if !recalled {
		t.Fatal("IsMessageRecalled() = false, want true")
	}
	if getCalls != 1 {
		t.Fatalf("getCalls = %d, want 1", getCalls)
	}
	if !p.isMessageRecalled("om_withdrawn") {
		t.Fatal("withdrawn message id was not cached after detection")
	}
}

func TestIsMessageRecalledDetectsDeletedMessageItem(t *testing.T) {
	const appID = "cli_recall_probe"
	const appSecret = "secret-recall-probe"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{
				"code":                0,
				"msg":                 "success",
				"expire":              7200,
				"tenant_access_token": "tenant-token",
			})
		case "/open-apis/im/v1/messages/om_deleted":
			writeJSON(t, w, map[string]any{
				"code": 0,
				"msg":  "success",
				"data": map[string]any{
					"items": []map[string]any{
						{
							"message_id": "om_deleted",
							"deleted":    true,
						},
					},
				},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	p := &Platform{
		platformName: "feishu",
		domain:       srv.URL,
		appID:        appID,
		appSecret:    appSecret,
		client: lark.NewClient(appID, appSecret,
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
		replayClient: lark.NewClient(appID, appSecret,
			lark.WithEnableTokenCache(false),
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
	}

	recalled, err := p.IsMessageRecalled(context.Background(), replyContext{messageID: "om_deleted", chatID: "oc_chat"})
	if err != nil {
		t.Fatalf("IsMessageRecalled() error = %v", err)
	}
	if !recalled {
		t.Fatal("IsMessageRecalled() = false, want true for deleted message item")
	}
	if !p.isMessageRecalled("om_deleted") {
		t.Fatal("deleted message id was not cached after detection")
	}
}

func TestExtractPostParts_TextOnly(t *testing.T) {
	p := &Platform{}
	post := &postLang{
		Title: "标题",
		Content: [][]postElement{
			{
				{Tag: "text", Text: "第一行"},
				{Tag: "text", Text: "接着"},
			},
			{
				{Tag: "text", Text: "第二行"},
			},
		},
	}
	texts, images := p.extractPostParts("", post)
	if len(texts) != 4 {
		t.Fatalf("expected 4 text parts, got %d", len(texts))
	}
	if texts[0] != "标题" {
		t.Errorf("expected title '标题', got %q", texts[0])
	}
	if texts[1] != "第一行" {
		t.Errorf("expected '第一行', got %q", texts[1])
	}
	if len(images) != 0 {
		t.Errorf("expected 0 images, got %d", len(images))
	}
}

func TestExtractPostParts_WithLink(t *testing.T) {
	p := &Platform{}
	post := &postLang{
		Content: [][]postElement{
			{
				{Tag: "text", Text: "点击 "},
				{Tag: "a", Text: "这里", Href: "https://example.com"},
			},
		},
	}
	texts, _ := p.extractPostParts("", post)
	if len(texts) != 2 {
		t.Fatalf("expected 2 text parts, got %d", len(texts))
	}
	if texts[0] != "点击 " || texts[1] != "[这里](https://example.com)" {
		t.Errorf("unexpected texts: %v", texts)
	}
}

func TestExtractPostParts_EmptyContent(t *testing.T) {
	p := &Platform{}
	post := &postLang{}
	texts, images := p.extractPostParts("", post)
	if len(texts) != 0 || len(images) != 0 {
		t.Errorf("expected empty results, got texts=%d images=%d", len(texts), len(images))
	}
}

func TestExtractPostParts_NoTitle(t *testing.T) {
	p := &Platform{}
	post := &postLang{
		Content: [][]postElement{
			{
				{Tag: "text", Text: "只有正文"},
			},
		},
	}
	texts, _ := p.extractPostParts("", post)
	if len(texts) != 1 || texts[0] != "只有正文" {
		t.Errorf("unexpected texts: %v", texts)
	}
}

func TestExtractPostParts_AtMention(t *testing.T) {
	p := &Platform{botOpenID: "ou_bot"}
	post := &postLang{
		Content: [][]postElement{
			{
				{Tag: "text", Text: "hi "},
				{Tag: "at", UserId: "ou_alice", UserName: "Alice"},
				{Tag: "text", Text: " and "},
				{Tag: "at", UserId: "all"},
				{Tag: "text", Text: " but not "},
				{Tag: "at", UserId: "ou_bot", UserName: "Bot"},
			},
		},
	}
	texts, _ := p.extractPostParts("", post)
	joined := strings.Join(texts, "")
	want := "hi @Alice and @all but not "
	if joined != want {
		t.Errorf("want %q, got %q", want, joined)
	}
}

func TestExtractPostParts_Markdown(t *testing.T) {
	p := &Platform{}
	post := &postLang{
		Content: [][]postElement{
			{{Tag: "markdown", Text: "**bold**"}},
		},
	}
	texts, _ := p.extractPostParts("", post)
	if len(texts) != 1 || texts[0] != "**bold**" {
		t.Errorf("unexpected texts: %v", texts)
	}
}

func TestParsePostContent_FlatFormat(t *testing.T) {
	p := &Platform{}
	raw := `{"title":"test","content":[[{"tag":"text","text":"hello"}]]}`
	texts, _ := p.parsePostContent("", raw)
	if len(texts) != 2 || texts[0] != "test" || texts[1] != "hello" {
		t.Errorf("unexpected result: %v", texts)
	}
}

func TestParsePostContent_LangKeyedFormat(t *testing.T) {
	p := &Platform{}
	raw := `{"zh_cn":{"title":"标题","content":[[{"tag":"text","text":"内容"}]]}}`
	texts, _ := p.parsePostContent("", raw)
	if len(texts) != 2 || texts[0] != "标题" || texts[1] != "内容" {
		t.Errorf("unexpected result: %v", texts)
	}
}

func TestParsePostContent_InvalidJSON(t *testing.T) {
	p := &Platform{}
	texts, images := p.parsePostContent("", "not json")
	if texts != nil || images != nil {
		t.Errorf("expected nil results for invalid json")
	}
}

func TestParseInlineMarkdown_Link(t *testing.T) {
	elements := parseInlineMarkdown("visit [Google](https://google.com) now")
	found := false
	for _, el := range elements {
		if el["tag"] == "a" && el["text"] == "Google" && el["href"] == "https://google.com" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected link element, got %v", elements)
	}
}

func TestParseInlineMarkdown_Italic(t *testing.T) {
	elements := parseInlineMarkdown("hello *world*")
	found := false
	for _, el := range elements {
		if styles, ok := el["style"].([]string); ok {
			for _, s := range styles {
				if s == "italic" && el["text"] == "world" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Errorf("expected italic element, got %v", elements)
	}
}

func TestParseInlineMarkdown_Strikethrough(t *testing.T) {
	elements := parseInlineMarkdown("hello ~~world~~")
	found := false
	for _, el := range elements {
		if styles, ok := el["style"].([]string); ok {
			for _, s := range styles {
				if s == "lineThrough" && el["text"] == "world" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Errorf("expected strikethrough element, got %v", elements)
	}
}

func TestPreprocessFeishuMarkdown_NewlineBeforeCodeFence(t *testing.T) {
	input := "some text```go\ncode\n```"
	out := preprocessFeishuMarkdown(input)
	if !strings.Contains(out, "text\n```go") {
		t.Errorf("expected newline before code fence, got %q", out)
	}
}

func TestPreprocessFeishuMarkdown_AlreadyNewline(t *testing.T) {
	input := "text\n```go\ncode\n```"
	out := preprocessFeishuMarkdown(input)
	if out != input {
		t.Errorf("should not change content that already has newlines, got %q", out)
	}
}

func TestPreprocessFeishuMarkdown_PreservesTablesAndHeadings(t *testing.T) {
	input := "## Title\n| A | B |\n|---|---|\n> quote"
	out := preprocessFeishuMarkdown(input)
	if !strings.Contains(out, "## Title") {
		t.Errorf("heading should be preserved, got %q", out)
	}
	if !strings.Contains(out, "| A | B |") {
		t.Errorf("table should be preserved, got %q", out)
	}
	if !strings.Contains(out, "> quote") {
		t.Errorf("blockquote should be preserved, got %q", out)
	}
}

func TestHasComplexMarkdown(t *testing.T) {
	if !hasComplexMarkdown("text\n```go\ncode\n```") {
		t.Error("should detect code blocks")
	}
	if !hasComplexMarkdown("| A | B |\n|---|---|") {
		t.Error("should detect tables")
	}
	if hasComplexMarkdown("**bold** and *italic*") {
		t.Error("should not detect simple markdown")
	}
}

func TestCountMarkdownTables(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{"no tables", "hello world", 0},
		{"one table", "| A | B |\n|---|---|\n| 1 | 2 |", 1},
		{"two tables separated by text", "| A |\n|---|\n\nsome text\n\n| B |\n|---|", 2},
		{"consecutive tables no gap", "| A |\n|---|\n| 1 |\n| B |\n|---|", 1},
		{"six tables", "| A |\n|---|\n\nx\n\n| B |\n|---|\n\nx\n\n| C |\n|---|\n\nx\n\n| D |\n|---|\n\nx\n\n| E |\n|---|\n\nx\n\n| F |\n|---|", 6},
		{"table inside code block is still counted", "```\n| A |\n```\n\n| B |\n|---|", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := countMarkdownTables(tt.input)
			if got != tt.want {
				t.Errorf("countMarkdownTables() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestBuildReplyContent_FallbackWhenManyTables(t *testing.T) {
	// Build content with 6 tables (exceeds the 5-table card limit).
	var sb strings.Builder
	for i := 0; i < 6; i++ {
		if i > 0 {
			sb.WriteString("\n\nsome text\n\n")
		}
		sb.WriteString("| H |\n|---|\n| V |")
	}
	content := sb.String()

	msgType, _ := buildReplyContent(content)
	if msgType == larkim.MsgTypeInteractive {
		t.Errorf("expected non-card message type for >5 tables, got interactive")
	}

	// With exactly 5 tables, card should still be used.
	sb.Reset()
	for i := 0; i < 5; i++ {
		if i > 0 {
			sb.WriteString("\n\nsome text\n\n")
		}
		sb.WriteString("| H |\n|---|\n| V |")
	}
	content5 := sb.String()

	msgType5, _ := buildReplyContent(content5)
	if msgType5 != larkim.MsgTypeInteractive {
		t.Errorf("expected interactive card for 5 tables, got %s", msgType5)
	}
}

func TestParseInlineMarkdown_BoldAndCode(t *testing.T) {
	elements := parseInlineMarkdown("**bold** and `code`")
	hasBold, hasCode := false, false
	for _, el := range elements {
		if styles, ok := el["style"].([]string); ok {
			for _, s := range styles {
				if s == "bold" && el["text"] == "bold" {
					hasBold = true
				}
				if s == "code" && el["text"] == "code" {
					hasCode = true
				}
			}
		}
	}
	if !hasBold || !hasCode {
		t.Errorf("expected bold and code, got %v", elements)
	}
}

func TestExtractPostPlainText_FlatFormat(t *testing.T) {
	content := `{"title":"公告","content":[[{"tag":"text","text":"第一段"}],[{"tag":"text","text":"第二段"}]]}`
	got := extractPostPlainText(content)
	if got != "公告\n第一段\n第二段" {
		t.Errorf("expected '公告\\n第一段\\n第二段', got %q", got)
	}
}

func TestExtractPostPlainText_LocaleWrapped(t *testing.T) {
	content := `{"zh_cn":{"title":"标题","content":[[{"tag":"text","text":"内容"}]]}}`
	got := extractPostPlainText(content)
	if got != "标题\n内容" {
		t.Errorf("expected '标题\\n内容', got %q", got)
	}
}

func TestExtractPostPlainText_NoTitle(t *testing.T) {
	content := `{"content":[[{"tag":"text","text":"仅内容"}]]}`
	got := extractPostPlainText(content)
	if got != "仅内容" {
		t.Errorf("expected '仅内容', got %q", got)
	}
}

func TestExtractPostPlainText_Empty(t *testing.T) {
	got := extractPostPlainText(`{}`)
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestExtractPostPlainText_LinkText(t *testing.T) {
	content := `{"content":[[{"tag":"text","text":"hello "},{"tag":"a","text":"link","href":"http://x.com"}]]}`
	got := extractPostPlainText(content)
	if got != "hello [link](http://x.com)" {
		t.Errorf("expected 'hello [link](http://x.com)', got %q", got)
	}
}

func TestExtractPostPlainText_AtMention(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"named", `{"content":[[{"tag":"text","text":"hi "},{"tag":"at","user_id":"ou_x","user_name":"Alice"}]]}`, "hi @Alice"},
		{"all", `{"content":[[{"tag":"at","user_id":"all"}]]}`, "@all"},
		{"fallback", `{"content":[[{"tag":"at","user_id":"ou_x"}]]}`, "@user"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractPostPlainText(tc.content); got != tc.want {
				t.Errorf("want %q, got %q", tc.want, got)
			}
		})
	}
}

func TestExtractPostPlainText_Markdown(t *testing.T) {
	content := `{"content":[[{"tag":"markdown","text":"**bold** and *italic*"}]]}`
	got := extractPostPlainText(content)
	if got != "**bold** and *italic*" {
		t.Errorf("expected markdown passthrough, got %q", got)
	}
}

func TestExtractPostPlainText_CodeBlock(t *testing.T) {
	content := `{"content":[[{"tag":"text","text":"see:"},{"tag":"code_block","language":"go","text":"fmt.Println()"}]]}`
	got := extractPostPlainText(content)
	// Same paragraph: inline elements are concatenated (no extra newline before the fence).
	want := "see:```go\nfmt.Println()\n```"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func strPtr(s string) *string { return &s }

func TestStripMentions(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		mentions  []*larkim.MentionEvent
		botOpenID string
		expected  string
	}{
		{
			name:      "no mentions",
			text:      "hello",
			mentions:  nil,
			botOpenID: "",
			expected:  "hello",
		},
		{
			name: "bot mention removed",
			text: "@_user_1 /help",
			mentions: []*larkim.MentionEvent{
				{Key: strPtr("@_user_1"), Id: &larkim.UserId{OpenId: strPtr("bot123")}, Name: strPtr("Bot")},
			},
			botOpenID: "bot123",
			expected:  "/help",
		},
		{
			name: "non-bot mention replaced with name",
			text: "assign to @_user_2",
			mentions: []*larkim.MentionEvent{
				{Key: strPtr("@_user_2"), Id: &larkim.UserId{OpenId: strPtr("user456")}, Name: strPtr("张三")},
			},
			botOpenID: "bot123",
			expected:  "assign to @张三",
		},
		{
			name: "bot removed and other preserved",
			text: "@_user_1 assign to @_user_2",
			mentions: []*larkim.MentionEvent{
				{Key: strPtr("@_user_1"), Id: &larkim.UserId{OpenId: strPtr("bot123")}, Name: strPtr("Bot")},
				{Key: strPtr("@_user_2"), Id: &larkim.UserId{OpenId: strPtr("user456")}, Name: strPtr("张三")},
			},
			botOpenID: "bot123",
			expected:  "assign to @张三",
		},
		{
			name: "mention with nil key skipped",
			text: "@_user_1 hello",
			mentions: []*larkim.MentionEvent{
				{Key: nil, Id: &larkim.UserId{OpenId: strPtr("bot123")}},
			},
			botOpenID: "bot123",
			expected:  "@_user_1 hello",
		},
		{
			name: "mention with no name fallback removed",
			text: "text @_user_3",
			mentions: []*larkim.MentionEvent{
				{Key: strPtr("@_user_3"), Id: &larkim.UserId{OpenId: strPtr("user789")}, Name: nil},
			},
			botOpenID: "bot123",
			expected:  "text",
		},
		{
			name: "empty botOpenID all non-named removed",
			text: "@_user_1 hello",
			mentions: []*larkim.MentionEvent{
				{Key: strPtr("@_user_1"), Id: &larkim.UserId{OpenId: strPtr("someone")}},
			},
			botOpenID: "",
			expected:  "hello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripMentions(tt.text, tt.mentions, tt.botOpenID)
			if got != tt.expected {
				t.Errorf("stripMentions() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestResolveBotSenderName(t *testing.T) {
	p := &Platform{peerBots: map[string]string{
		"cli_known": "Jeeves",
		"cli_other": "Ivy",
	}}
	tests := []struct {
		name  string
		appID string
		want  string
	}{
		{"empty app id falls back to Bot", "", "Bot"},
		{"known app id resolves to alias", "cli_known", "Jeeves"},
		{"another known app id", "cli_other", "Ivy"},
		{"unknown app id surfaces id", "cli_unknown", "Bot[cli_unknown]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := p.resolveBotSenderName(tt.appID)
			if got != tt.want {
				t.Errorf("resolveBotSenderName(%q) = %q, want %q", tt.appID, got, tt.want)
			}
		})
	}
}

func TestResolveBotSenderName_NilMap(t *testing.T) {
	p := &Platform{}
	if got := p.resolveBotSenderName("cli_any"); got != "Bot[cli_any]" {
		t.Errorf("nil peerBots: got %q, want %q", got, "Bot[cli_any]")
	}
	if got := p.resolveBotSenderName(""); got != "Bot" {
		t.Errorf("nil peerBots + empty id: got %q, want %q", got, "Bot")
	}
}

func TestIsAttachmentMsgType(t *testing.T) {
	tests := []struct {
		msgType string
		want    bool
	}{
		{"image", true},
		{"file", true},
		{"audio", true},
		{"media", true},
		{"text", false},
		{"post", false},
		{"sticker", false},
		{"merge_forward", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isAttachmentMsgType(tt.msgType); got != tt.want {
			t.Errorf("isAttachmentMsgType(%q) = %v, want %v", tt.msgType, got, tt.want)
		}
	}
}

func TestMarkAndIsActiveThreadSession(t *testing.T) {
	const threadKey = "feishu:oc_chat:root:om_root"
	const directKey = "feishu:oc_chat:ou_user"

	t.Run("thread isolation disabled is no-op", func(t *testing.T) {
		p := &Platform{threadIsolation: false}
		p.markThreadSessionActive(threadKey)
		if p.isActiveThreadSession(threadKey) {
			t.Fatal("expected no-op when thread_isolation is off")
		}
	})

	t.Run("non-thread sessionKey is ignored", func(t *testing.T) {
		p := &Platform{threadIsolation: true}
		p.markThreadSessionActive(directKey)
		if p.isActiveThreadSession(directKey) {
			t.Fatal("expected non-thread sessionKey to be ignored")
		}
	})

	t.Run("thread sessionKey is recorded", func(t *testing.T) {
		p := &Platform{threadIsolation: true}
		if p.isActiveThreadSession(threadKey) {
			t.Fatal("thread should not be active before mark")
		}
		p.markThreadSessionActive(threadKey)
		if !p.isActiveThreadSession(threadKey) {
			t.Fatal("thread should be active after mark")
		}
	})
}

// TestOnMessageThreadIsolationAdmitsAttachmentWithoutMention covers the fix
// for the case where a user @mentions the bot in a thread, then drops follow-up
// images into the same thread without re-mentioning. Pre-fix, those images
// were silently dropped by the group @bot filter; post-fix they should pass
// through and be dispatched.
func TestOnMessageThreadIsolationAdmitsAttachmentWithoutMention(t *testing.T) {
	const appID = "cli_thread_admit"
	const appSecret = "secret-thread-admit"
	const botOpenID = "ou_bot"
	const userOpenID = "ou_user"
	const chatID = "oc_chat"
	const rootMsgID = "om_root"
	const imageKey = "img_in_thread"

	imageBytes := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			w.Header().Set("Content-Type", "application/json")
			writeJSON(t, w, map[string]any{
				"code":                0,
				"msg":                 "success",
				"expire":              7200,
				"tenant_access_token": "tenant-token",
			})
		case strings.HasSuffix(r.URL.Path, "/resources/"+imageKey):
			w.Header().Set("Content-Type", "image/png")
			if _, err := w.Write(imageBytes); err != nil {
				t.Fatalf("write image: %v", err)
			}
		default:
			// Unknown calls (e.g. user/chat info lookups) return empty success
			// so dispatch can continue past optional metadata fetches.
			w.Header().Set("Content-Type", "application/json")
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success", "data": map[string]any{}})
		}
	}))
	defer srv.Close()

	received := make(chan *core.Message, 8)
	p := &Platform{
		platformName:    "feishu",
		domain:          srv.URL,
		appID:           appID,
		appSecret:       appSecret,
		botOpenID:       botOpenID,
		threadIsolation: true,
		dedup:           &core.MessageDedup{},
		client: lark.NewClient(appID, appSecret,
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
		handler: func(_ core.Platform, msg *core.Message) {
			received <- msg
		},
	}

	chatType := "group"
	senderType := "user"
	now := time.Now().UnixMilli()
	createTime := func() *string {
		s := strconv.FormatInt(now, 10)
		now++
		return &s
	}

	buildEvent := func(msgID, msgType, content string, mentions []*larkim.MentionEvent, rootID string) *larkim.P2MessageReceiveV1 {
		ev := &larkim.P2MessageReceiveV1{
			Event: &larkim.P2MessageReceiveV1Data{
				Sender: &larkim.EventSender{
					SenderId:   &larkim.UserId{OpenId: stringPtr(userOpenID)},
					SenderType: &senderType,
				},
				Message: &larkim.EventMessage{
					MessageId:   stringPtr(msgID),
					ChatId:      stringPtr(chatID),
					ChatType:    &chatType,
					MessageType: stringPtr(msgType),
					Content:     stringPtr(content),
					CreateTime:  createTime(),
					Mentions:    mentions,
				},
			},
		}
		if rootID != "" {
			ev.Event.Message.RootId = stringPtr(rootID)
		}
		return ev
	}

	botMention := []*larkim.MentionEvent{
		{
			Key:  stringPtr("@_user_1"),
			Id:   &larkim.UserId{OpenId: stringPtr(botOpenID)},
			Name: stringPtr("claude code"),
		},
	}

	// Step 1: opening message @mentions the bot — establishes the thread.
	if err := p.onMessage(context.Background(), buildEvent(rootMsgID, "text", `{"text":"@_user_1 看看这个"}`, botMention, "")); err != nil {
		t.Fatalf("onMessage(root) error = %v", err)
	}
	threadKey := "feishu:" + chatID + ":root:" + rootMsgID
	if !p.isActiveThreadSession(threadKey) {
		t.Fatalf("thread %q should be marked active after @bot text", threadKey)
	}
	select {
	case <-received:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the opening @bot text to dispatch")
	}

	// Step 2: follow-up image in the same thread, no @mention — should pass through.
	imgContent := `{"image_key":"` + imageKey + `"}`
	if err := p.onMessage(context.Background(), buildEvent("om_thread_img", "image", imgContent, nil, rootMsgID)); err != nil {
		t.Fatalf("onMessage(image in thread) error = %v", err)
	}
	select {
	case msg := <-received:
		if msg.MessageID != "om_thread_img" {
			t.Fatalf("expected dispatched image om_thread_img, got %q", msg.MessageID)
		}
		if len(msg.Images) != 1 {
			t.Fatalf("expected dispatched image to carry 1 attachment, got %d", len(msg.Images))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected image in active thread to be dispatched without @mention")
	}

	// Step 3: image in a *different* thread without @mention — should still be dropped.
	if err := p.onMessage(context.Background(), buildEvent("om_other_img", "image", imgContent, nil, "om_other_root")); err != nil {
		t.Fatalf("onMessage(image in unrelated thread) error = %v", err)
	}
	select {
	case msg := <-received:
		t.Fatalf("image in unrelated thread should be dropped, but handler received %q", msg.MessageID)
	case <-time.After(300 * time.Millisecond):
		// expected: nothing dispatched
	}

	// Step 4: text without @mention in the active thread — should be dropped
	// (only attachments are admitted; otherwise unrelated thread chatter would
	// flood the agent).
	if err := p.onMessage(context.Background(), buildEvent("om_thread_text", "text", `{"text":"刚才那张图能看到吗"}`, nil, rootMsgID)); err != nil {
		t.Fatalf("onMessage(text in thread without mention) error = %v", err)
	}
	select {
	case msg := <-received:
		t.Fatalf("text in active thread without @mention should be dropped, got %q", msg.MessageID)
	case <-time.After(300 * time.Millisecond):
		// expected
	}
}

func extractBasePlatform(p core.Platform) *Platform {
	if fp, ok := p.(*Platform); ok {
		return fp
	}
	if ip, ok := p.(*interactivePlatform); ok {
		return ip.Platform
	}
	return nil
}

func TestNewPlatform_RequireMentionFalseAliasesGroupReplyAll(t *testing.T) {
	// Regression test for #1141: users set require_mention = false but feishu
	// reads group_reply_all. The two options must be equivalent so that
	// group messages without @mention are NOT silently dropped.
	p, err := newPlatform("feishu", lark.FeishuBaseUrl, map[string]any{
		"app_id":          "cli_test",
		"app_secret":      "secret",
		"require_mention": false,
	})
	if err != nil {
		t.Fatalf("newPlatform error: %v", err)
	}
	fp := extractBasePlatform(p)
	if fp == nil {
		t.Fatal("expected *Platform or *interactivePlatform")
	}
	if !fp.groupReplyAll {
		t.Error("require_mention=false should set groupReplyAll=true, but it is false")
	}
}

func TestNewPlatform_RequireMentionTrueDoesNotForceGroupReplyAll(t *testing.T) {
	// require_mention = true (the default) must NOT set groupReplyAll.
	p, err := newPlatform("feishu", lark.FeishuBaseUrl, map[string]any{
		"app_id":          "cli_test",
		"app_secret":      "secret",
		"require_mention": true,
	})
	if err != nil {
		t.Fatalf("newPlatform error: %v", err)
	}
	fp := extractBasePlatform(p)
	if fp == nil {
		t.Fatal("expected *Platform or *interactivePlatform")
	}
	if fp.groupReplyAll {
		t.Error("require_mention=true should leave groupReplyAll=false, but it is true")
	}
}

// TestDispatchMessageCoalescesImageBatch covers issue #1395: when the Feishu
// mobile client sends N images in quick succession, each image arrives as a
// separate message event with very close create_time values. Dispatching each
// immediately caused core/engine's create_time watermark (PR #1168) to drop
// the oldest image, so the agent only saw N-1 images. After the fix, all N
// images within imageBatchWindow should be coalesced into ONE dispatched
// core.Message with N image attachments.
func TestDispatchMessageCoalescesImageBatch(t *testing.T) {
	const appID = "cli_batch_img"
	const appSecret = "secret-batch-img"
	const chatID = "oc_batch"
	const userID = "ou_user"

	tests := []struct {
		name      string
		imageKeys []string
		wantCount int
		wantMsgs  int
	}{
		{name: "2 images", imageKeys: []string{"img_1", "img_2"}, wantCount: 2, wantMsgs: 1},
		{name: "3 images", imageKeys: []string{"img_a", "img_b", "img_c"}, wantCount: 3, wantMsgs: 1},
		{name: "4 images", imageKeys: []string{"i1", "i2", "i3", "i4"}, wantCount: 4, wantMsgs: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Per-image payload bytes so we can verify order preservation.
			imageBytes := map[string][]byte{}
			for i, k := range tc.imageKeys {
				imageBytes[k] = []byte{0x89, 'P', 'N', 'G', byte(i + 1), '\r', '\n', 0x1a, '\n'}
			}

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
					w.Header().Set("Content-Type", "application/json")
					writeJSON(t, w, map[string]any{
						"code":                0,
						"msg":                 "success",
						"expire":              7200,
						"tenant_access_token": "tenant-token",
					})
				case strings.Contains(r.URL.Path, "/resources/"):
					key := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
					data, ok := imageBytes[key]
					if !ok {
						t.Fatalf("unexpected image key %q", key)
					}
					w.Header().Set("Content-Type", "image/png")
					if _, err := w.Write(data); err != nil {
						t.Fatalf("write image: %v", err)
					}
				default:
					w.Header().Set("Content-Type", "application/json")
					writeJSON(t, w, map[string]any{"code": 0, "msg": "success", "data": map[string]any{}})
				}
			}))
			defer srv.Close()

			received := make(chan *core.Message, tc.wantCount+1)
			p := &Platform{
				platformName: "feishu",
				domain:       srv.URL,
				appID:        appID,
				appSecret:    appSecret,
				dedup:        &core.MessageDedup{},
				client: lark.NewClient(appID, appSecret,
					lark.WithOpenBaseUrl(srv.URL),
					lark.WithHttpClient(srv.Client()),
				),
				handler: func(_ core.Platform, msg *core.Message) {
					received <- msg
				},
				imageBatch: make(map[string]*imageBatchEntry),
			}

			sessionKey := "feishu:" + chatID + ":" + userID
			for i, key := range tc.imageKeys {
				msgID := "om_img_" + strconv.Itoa(i)
				content := `{"image_key":"` + key + `"}`
				p.dispatchMessage(
					context.Background(),
					"image",
					content,
					nil,
					msgID,
					sessionKey,
					userID,
					chatID,
					replyContext{messageID: msgID, chatID: chatID, sessionKey: sessionKey},
					"", // no parentID so we exercise the batch path
					int64(1710000000000+i),
				)
			}

			// Collect dispatched messages with a generous timeout that comfortably
			// exceeds imageBatchWindow (150ms) but still finishes the test quickly.
			var dispatched []*core.Message
			deadline := time.After(2 * time.Second)
			for len(dispatched) < tc.wantMsgs {
				select {
				case msg := <-received:
					dispatched = append(dispatched, msg)
				case <-deadline:
					t.Fatalf("got %d messages, want %d (timeout)", len(dispatched), tc.wantMsgs)
				}
			}

			// Allow late stragglers to surface so we can fail loudly if batching
			// leaked more than one dispatch.
			select {
			case extra := <-received:
				t.Fatalf("unexpected extra dispatched message %q with %d images", extra.MessageID, len(extra.Images))
			case <-time.After(imageBatchWindow + 100*time.Millisecond):
			}

			if len(dispatched) != 1 {
				t.Fatalf("dispatched %d messages, want exactly 1 (batch should coalesce)", len(dispatched))
			}
			msg := dispatched[0]
			if len(msg.Images) != tc.wantCount {
				t.Fatalf("merged message has %d images, want %d", len(msg.Images), tc.wantCount)
			}
			for i, img := range msg.Images {
				want := imageBytes[tc.imageKeys[i]]
				if string(img.Data) != string(want) {
					t.Errorf("image[%d] data = %x, want %x (order must match send order)", i, img.Data, want)
				}
			}
		})
	}
}

// TestDispatchMessageSingleImageRegression ensures the single-image path still
// works after introducing the image-batch buffer (issue #1395). A single image
// must still produce one dispatched core.Message.
func TestDispatchMessageSingleImageRegression(t *testing.T) {
	const appID = "cli_single_img"
	const appSecret = "secret-single-img"
	const imageKey = "img_single"

	imageBytes := []byte{0x89, 'P', 'N', 'G', 'S', '\r', '\n', 0x1a, '\n'}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			w.Header().Set("Content-Type", "application/json")
			writeJSON(t, w, map[string]any{
				"code":                0,
				"msg":                 "success",
				"expire":              7200,
				"tenant_access_token": "tenant-token",
			})
		case strings.HasSuffix(r.URL.Path, "/resources/"+imageKey):
			w.Header().Set("Content-Type", "image/png")
			if _, err := w.Write(imageBytes); err != nil {
				t.Fatalf("write image: %v", err)
			}
		default:
			w.Header().Set("Content-Type", "application/json")
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success", "data": map[string]any{}})
		}
	}))
	defer srv.Close()

	got := make(chan *core.Message, 1)
	p := &Platform{
		platformName: "feishu",
		domain:       srv.URL,
		appID:        appID,
		appSecret:    appSecret,
		dedup:        &core.MessageDedup{},
		client: lark.NewClient(appID, appSecret,
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
		handler: func(_ core.Platform, msg *core.Message) {
			got <- msg
		},
		imageBatch: make(map[string]*imageBatchEntry),
	}

	p.dispatchMessage(
		context.Background(),
		"image",
		`{"image_key":"`+imageKey+`"}`,
		nil,
		"om_single",
		"feishu:oc_single:ou_user",
		"ou_user",
		"oc_single",
		replyContext{messageID: "om_single", chatID: "oc_single", sessionKey: "feishu:oc_single:ou_user"},
		"", 0,
	)

	select {
	case msg := <-got:
		if msg.MessageID != "om_single" {
			t.Errorf("MessageID = %q, want om_single", msg.MessageID)
		}
		if len(msg.Images) != 1 {
			t.Fatalf("len(Images) = %d, want 1", len(msg.Images))
		}
		if string(msg.Images[0].Data) != string(imageBytes) {
			t.Fatal("image data did not match downloaded resource")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for single image to dispatch")
	}
}

// TestDispatchMessageQuotedImageNotBatched ensures that quoted (parentID != "")
// images still follow the legacy synchronous path: one dispatched message per
// image, with quoted context attached. Batching must not affect this code path.
func TestDispatchMessageQuotedImageNotBatched(t *testing.T) {
	const appID = "cli_quoted_img"
	const appSecret = "secret-quoted-img"
	const parentMessageID = "om_parent_quoted"
	const imageKey = "img_quoted"

	imageData := []byte{0x89, 'P', 'N', 'G', 'Q', '\r', '\n', 0x1a, '\n'}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			w.Header().Set("Content-Type", "application/json")
			writeJSON(t, w, map[string]any{
				"code":                0,
				"msg":                 "success",
				"expire":              7200,
				"tenant_access_token": "tenant-token",
			})
		case r.URL.Path == "/open-apis/im/v1/messages/"+parentMessageID:
			w.Header().Set("Content-Type", "application/json")
			writeJSON(t, w, map[string]any{
				"code": 0,
				"msg":  "success",
				"data": map[string]any{
					"items": []map[string]any{
						{
							"msg_type":  "text",
							"parent_id": "",
							"sender":    map[string]any{"id": "", "sender_type": "user"},
							"body":      map[string]any{"content": `{"text":"请看图"}`},
						},
					},
				},
			})
		case strings.HasSuffix(r.URL.Path, "/resources/"+imageKey):
			w.Header().Set("Content-Type", "image/png")
			if _, err := w.Write(imageData); err != nil {
				t.Fatalf("write image: %v", err)
			}
		default:
			w.Header().Set("Content-Type", "application/json")
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success", "data": map[string]any{}})
		}
	}))
	defer srv.Close()

	got := make(chan *core.Message, 1)
	p := &Platform{
		platformName: "feishu",
		domain:       srv.URL,
		appID:        appID,
		appSecret:    appSecret,
		client: lark.NewClient(appID, appSecret,
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
		handler: func(_ core.Platform, msg *core.Message) {
			got <- msg
		},
		imageBatch: make(map[string]*imageBatchEntry),
	}

	p.dispatchMessage(
		context.Background(),
		"image",
		`{"image_key":"`+imageKey+`"}`,
		nil,
		"om_quoted_child",
		"feishu:oc_chat:ou_user",
		"ou_user",
		"oc_chat",
		replyContext{messageID: "om_quoted_child", chatID: "oc_chat", sessionKey: "feishu:oc_chat:ou_user"},
		parentMessageID, 0,
	)

	select {
	case msg := <-got:
		if len(msg.Images) != 1 {
			t.Fatalf("quoted image path: len(Images) = %d, want 1 (no batching)", len(msg.Images))
		}
		if !strings.Contains(msg.ExtraContent, "请看图") {
			t.Fatalf("ExtraContent = %q, want quoted text context", msg.ExtraContent)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for quoted image to dispatch")
	}
}

// TestFlushImageBatchesStopsPendingTimers ensures Stop()-style flush
// synchronously dispatches all buffered images without losing any.
func TestFlushImageBatchesStopsPendingTimers(t *testing.T) {
	const appID = "cli_flush"
	const appSecret = "secret-flush"
	const imageKey = "img_flush"

	imageBytes := []byte{0x89, 'P', 'N', 'G', 'F', '\r', '\n', 0x1a, '\n'}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			w.Header().Set("Content-Type", "application/json")
			writeJSON(t, w, map[string]any{
				"code":                0,
				"msg":                 "success",
				"expire":              7200,
				"tenant_access_token": "tenant-token",
			})
		case strings.HasSuffix(r.URL.Path, "/resources/"+imageKey):
			w.Header().Set("Content-Type", "image/png")
			if _, err := w.Write(imageBytes); err != nil {
				t.Fatalf("write image: %v", err)
			}
		default:
			w.Header().Set("Content-Type", "application/json")
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success", "data": map[string]any{}})
		}
	}))
	defer srv.Close()

	received := make(chan *core.Message, 2)
	p := &Platform{
		platformName: "feishu",
		domain:       srv.URL,
		appID:        appID,
		appSecret:    appSecret,
		dedup:        &core.MessageDedup{},
		client: lark.NewClient(appID, appSecret,
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
		handler: func(_ core.Platform, msg *core.Message) {
			received <- msg
		},
		imageBatch: make(map[string]*imageBatchEntry),
	}

	// Buffer a single image but DON'T wait for the timer — instead flush manually.
	p.bufferImage("feishu:oc_flush:ou_user", &imageBatchEntry{
		sessionKey:   "feishu:oc_flush:ou_user",
		userID:       "ou_user",
		chatName:     "oc_flush",
		rctx:         replyContext{messageID: "om_flush", chatID: "oc_flush", sessionKey: "feishu:oc_flush:ou_user"},
		images:       []core.ImageAttachment{{MimeType: "image/png", Data: imageBytes}},
		messageIDs:   []string{"om_flush"},
		createTimeMs: 1710000000000,
	})

	// Confirm the buffer is populated and the timer is pending.
	p.imageBatchMu.Lock()
	if len(p.imageBatch) != 1 {
		p.imageBatchMu.Unlock()
		t.Fatalf("imageBatch size = %d, want 1 before flush", len(p.imageBatch))
	}
	p.imageBatchMu.Unlock()

	p.flushImageBatches()

	// After flush, the map must be empty AND no timer should be left pending.
	p.imageBatchMu.Lock()
	batchSize := len(p.imageBatch)
	p.imageBatchMu.Unlock()
	if batchSize != 0 {
		t.Fatalf("imageBatch size = %d after flushImageBatches, want 0", batchSize)
	}

	select {
	case msg := <-received:
		if len(msg.Images) != 1 {
			t.Fatalf("flushed message has %d images, want 1", len(msg.Images))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for flushed batch to dispatch")
	}

	// No additional dispatches after flush.
	select {
	case extra := <-received:
		t.Fatalf("unexpected extra message after flush: %+v", extra)
	case <-time.After(imageBatchWindow + 100*time.Millisecond):
	}
}

// TestFlushImageBatchesEmptySafe ensures flushImageBatches is a safe no-op
// when nothing is buffered (e.g. Stop() called on an idle platform).
func TestFlushImageBatchesEmptySafe(t *testing.T) {
	p := &Platform{
		platformName: "feishu",
		imageBatch:   make(map[string]*imageBatchEntry),
	}
	// Should not panic, should not block.
	p.flushImageBatches()
}
