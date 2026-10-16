package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

const testInteractiveBotID = "ou_interactive_bot"

// errTransport fails every request instantly, keeping offline tests hermetic.
type errTransport struct{}

func (errTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("no network in test")
}

// testInteractiveCardContent is a receive-format card covering every element
// tag the parser supports, modelled on the Feishu "Receive message content
// structure" doc example.
const testInteractiveCardContent = `{
  "title": "周报卡片",
  "elements": [
    [{"tag": "text", "text": "本周进展如下"}],
    [
      {"tag": "a", "href": "https://example.com/report", "text": "查看详情"},
      {"tag": "at", "user_id": "@_user_1", "user_name": ""},
      {"tag": "at", "user_id": "all"}
    ],
    [
      {"tag": "button", "text": "批准", "type": "primary"},
      {"tag": "button", "text": "驳回", "type": "danger"}
    ],
    [{"tag": "hr"}],
    [
      {"tag": "text", "text": "截图"},
      {"tag": "img", "image_key": "img_test_key_1"}
    ],
    [{"tag": "note", "elements": [{"tag": "text", "text": "备注信息"}]}],
    [{"tag": "select_static", "placeholder": "请选择负责人", "options": ["张三", "李四"]}],
    [{"tag": "overflow", "options": ["打开目录", "查看文档"]}],
    [{"tag": "date_picker", "placeholder": "请选择日期", "initial_date": "2026-10-01"}]
  ]
}`

func TestExtractInteractiveReceiveParts_FullCard(t *testing.T) {
	parts, imageKeys := extractInteractiveReceiveParts(testInteractiveCardContent)

	wantParts := []string{
		"周报卡片",
		"本周进展如下",
		"[查看详情](https://example.com/report)",
		"@_user_1",
		"@all",
		"批准",
		"驳回",
		"---",
		"截图",
		"备注信息",
		"请选择负责人",
		"[options: 张三, 李四]",
		"[options: 打开目录, 查看文档]",
		"请选择日期",
	}
	if len(parts) != len(wantParts) {
		t.Fatalf("parts = %q, want %q", parts, wantParts)
	}
	for i := range wantParts {
		if parts[i] != wantParts[i] {
			t.Fatalf("parts[%d] = %q, want %q (full: %q)", i, parts[i], wantParts[i], parts)
		}
	}
	if len(imageKeys) != 1 || imageKeys[0] != "img_test_key_1" {
		t.Fatalf("imageKeys = %q, want [img_test_key_1]", imageKeys)
	}
}

func TestExtractInteractiveReceiveParts_FlatElements(t *testing.T) {
	parts, imageKeys := extractInteractiveReceiveParts(
		`{"title":"T","elements":[{"tag":"text","text":"hi"},{"tag":"img","image_key":"k1"}]}`)
	if len(parts) != 2 || parts[0] != "T" || parts[1] != "hi" {
		t.Fatalf("parts = %q", parts)
	}
	if len(imageKeys) != 1 || imageKeys[0] != "k1" {
		t.Fatalf("imageKeys = %q", imageKeys)
	}
}

func TestExtractInteractiveReceiveParts_InvalidJSON(t *testing.T) {
	for _, in := range []string{"", "{not json", `{"foo":1}`, `{"title":"","elements":[]}`} {
		if parts, keys := extractInteractiveReceiveParts(in); len(parts) != 0 || len(keys) != 0 {
			t.Fatalf("extractInteractiveReceiveParts(%q) = %q, %q; want empty", in, parts, keys)
		}
	}
}

func TestExtractInteractiveReceiveText_ImagePlaceholderAndFallback(t *testing.T) {
	got := extractInteractiveReceiveText(`{"title":"T","elements":[[{"tag":"img","image_key":"k"}]]}`)
	if got != "T\n[image]" {
		t.Fatalf("got %q, want %q", got, "T\n[image]")
	}
	// Not receive format and not a card either: legacy placeholder preserved.
	if got := extractInteractiveReceiveText(`{not json`); got != "[interactive card]" {
		t.Fatalf("got %q, want [interactive card]", got)
	}
}

func TestIsGroupHistoryMessageType_Interactive(t *testing.T) {
	if !isGroupHistoryMessageType("interactive") {
		t.Fatal("interactive should be a group-history message type")
	}
}

func TestHistoryText_InteractiveStripsBotMention(t *testing.T) {
	p := &Platform{platformName: "feishu", botOpenID: testInteractiveBotID}
	mentions := []*larkim.MentionEvent{
		{Key: strPtr("@_user_1"), Id: &larkim.UserId{OpenId: strPtr(testInteractiveBotID)}},
		{Key: strPtr("@_user_2"), Id: &larkim.UserId{OpenId: strPtr("ou_other")}, Name: strPtr("李四")},
	}
	content := `{"title":"审批单","elements":[[{"tag":"text","text":"请审批"}],[{"tag":"at","user_id":"@_user_1"},{"tag":"at","user_id":"@_user_2"}]]}`
	got := p.historyText("interactive", content, mentions)
	// Bot token removed, other user resolved to @name.
	if strings.Contains(got, "@_user_1") || strings.Contains(got, testInteractiveBotID) {
		t.Fatalf("bot mention not stripped: %q", got)
	}
	if !strings.Contains(got, "@李四") || !strings.Contains(got, "请审批") {
		t.Fatalf("unexpected history text: %q", got)
	}
}

func TestHistoryText_InteractiveUnparseable(t *testing.T) {
	p := &Platform{platformName: "feishu"}
	if got := p.historyText("interactive", `{not json`, nil); got != "" {
		t.Fatalf("got %q, want empty (marker must not pollute history)", got)
	}
}

// testCard2RawJSON is a compact Card 2.0 original covering header,
// column_set > column > markdown > heading/plain_text, table, at, img and
// button, mirroring the real user_card_content serialization (camelCase
// under property).
const testCard2RawJSON = `{
  "schema": "2.0",
  "header": {"property": {"title": {"property": {"content": "测试报告"}}}},
  "body": {"property": {"elements": [
    {"tag": "column_set", "property": {"columns": [
      {"tag": "column", "property": {"elements": [
        {"tag": "markdown", "property": {"elements": [
          {"tag": "heading", "property": {"level": 2, "elements": [
            {"tag": "plain_text", "property": {"content": "12%"}}]}},
          {"tag": "plain_text", "property": {"content": "通过率"}}]}}]}},
      {"tag": "column", "property": {"elements": [
        {"tag": "markdown", "property": {"elements": [
          {"tag": "plain_text", "property": {"content": "222"}}]}}]}}]}},
    {"tag": "table", "property": {"columns": [
      {"displayName": "用例", "name": "case"},
      {"displayName": "结果", "name": "result"}],
      "rows": [{"case": {"data": "登录"}, "result": {"data": "通过"}}]}},
    {"tag": "markdown", "property": {"elements": [
      {"tag": "at", "property": {"userID": "ou_owner"}}]}},
    {"tag": "img", "property": {"imageKey": "img_real_1"}},
    {"tag": "button", "property": {"content": "查看详情",
      "behaviors": [{"type": "open_url", "url": "https://example.com/r"}]}}
  ]}}
}`

func TestExtractCard2RawParts_ReportCard(t *testing.T) {
	keyMap := map[string]string{"ou_owner": "@_user_9"}
	parts, keys := extractCard2RawParts(testCard2RawJSON, keyMap)
	wantParts := []string{
		"测试报告",
		"## 12%",
		"通过率",
		"222",
		"| 用例 | 结果 |",
		"| --- | --- |",
		"| 登录 | 通过 |",
		"@_user_9",
		"[查看详情](https://example.com/r)",
	}
	if len(parts) != len(wantParts) {
		t.Fatalf("parts = %q, want %q", parts, wantParts)
	}
	for i := range wantParts {
		if parts[i] != wantParts[i] {
			t.Fatalf("parts[%d] = %q, want %q (full: %q)", i, parts[i], wantParts[i], parts)
		}
	}
	if len(keys) != 1 || keys[0] != "img_real_1" {
		t.Fatalf("keys = %q, want [img_real_1]", keys)
	}
}

func TestExtractCard2RawParts_WrapperAndNonCard(t *testing.T) {
	wrapped := `{"json_card": ` + strconv.Quote(testCard2RawJSON) + `}`
	parts, _ := extractCard2RawParts(wrapped, nil)
	if len(parts) == 0 || parts[0] != "测试报告" {
		t.Fatalf("wrapper parts = %q, want header first", parts)
	}
	// Lossy receive-format envelope (no body/schema) must fall through.
	if p, k := extractCard2RawParts(`{"title":"标题","elements":[[{"tag":"text","text":"hi"}]]}`, nil); len(p) != 0 || len(k) != 0 {
		t.Fatalf("degraded envelope = %q, %q; want nil nil", p, k)
	}
	if p, k := extractCard2RawParts(`{not json`, nil); len(p) != 0 || len(k) != 0 {
		t.Fatalf("garbage = %q, %q; want nil nil", p, k)
	}
}

// testCard2FlatJSON mirrors the real user_card_content serialization: children
// mounted directly (no property wrapper), snake_case table headers, markdown
// with inline <at> mentions and <font> styling.
const testCard2FlatJSON = `{
  "schema": "2.0",
  "header": {"template": "yellow", "title": {"content": "标题", "tag": "plain_text"}},
  "body": {"direction": "vertical", "elements": [
    {"tag": "column_set", "element_id": "_2", "columns": [
      {"tag": "column", "elements": [
        {"tag": "markdown", "content": "\n## 12%\n"},
        {"tag": "markdown", "content": "<font color='grey'>通过率</font>"}]},
      {"tag": "column", "elements": [
        {"tag": "markdown", "content": "\n## 222\n"}]}]},
    {"tag": "table", "element_id": "custom_id", "columns": [
      {"data_type": "text", "display_name": "用例名称", "name": "case_name"},
      {"data_type": "text", "display_name": "失败原因", "name": "failure_reason"}],
      "rows": [{"case_name": "登录", "failure_reason": "超时"}, {"case_name": "支付", "failure_reason": 500}]},
    {"tag": "markdown", "content": "<at id=ou_owner></at> 后续跟进"}
  ]}
}`

func TestExtractCard2RawParts_FlatCard(t *testing.T) {
	keyMap := map[string]string{"ou_owner": "@_user_9"}
	parts, keys := extractCard2RawParts(testCard2FlatJSON, keyMap)
	wantParts := []string{
		"标题",
		"\n## 12%\n",
		"通过率",
		"\n## 222\n",
		"| 用例名称 | 失败原因 |",
		"| --- | --- |",
		"| 登录 | 超时 |",
		"| 支付 | 500 |",
		"@_user_9 后续跟进",
	}
	if len(parts) != len(wantParts) {
		t.Fatalf("parts = %q, want %q", parts, wantParts)
	}
	for i := range wantParts {
		if parts[i] != wantParts[i] {
			t.Fatalf("parts[%d] = %q, want %q (full: %q)", i, parts[i], wantParts[i], parts)
		}
	}
	if len(keys) != 0 {
		t.Fatalf("keys = %q, want empty", keys)
	}
	for _, leak := range []string{"<font", "<at", "display_name", "element_id"} {
		if strings.Contains(strings.Join(parts, "\n"), leak) {
			t.Fatalf("markup leaked into parts: %q", leak)
		}
	}
}

// TestDispatchMessageInteractive covers live dispatch of a card: title, text,
// link, @bot (stripped), other @user (kept as @name), buttons, and one image
// downloaded through the resource stub.
func TestDispatchMessageInteractive(t *testing.T) {
	const appID = "cli_interactive"
	const appSecret = "secret-interactive"
	const messageID = "om_interactive_1"
	const imageKey = "img_interactive_1"
	imgBytes := []byte("\x89PNG\r\n\x1a\nfake-png-bytes")

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
		case r.URL.Path == "/open-apis/im/v1/messages/"+messageID+"/resources/"+imageKey:
			if r.URL.Query().Get("type") != "image" {
				t.Fatalf("resource type = %q, want image", r.URL.Query().Get("type"))
			}
			if _, err := w.Write(imgBytes); err != nil {
				t.Fatalf("write image: %v", err)
			}
		case r.URL.Path == "/open-apis/im/v1/messages/"+messageID &&
			r.URL.Query().Get("card_msg_content_type") == "user_card_content":
			// Raw fetch unavailable here: exercises the degraded-body fallback.
			w.Header().Set("Content-Type", "application/json")
			writeJSON(t, w, map[string]any{"code": 230001, "msg": "param error"})
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
		platformName:         "feishu",
		domain:               srv.URL,
		appID:                appID,
		appSecret:            appSecret,
		botOpenID:            testInteractiveBotID,
		resourceDownloadHTTP: srv.Client(),
		client: lark.NewClient(appID, appSecret,
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
		handler: func(_ core.Platform, msg *core.Message) {
			got <- msg
		},
	}

	mentions := []*larkim.MentionEvent{
		{Key: strPtr("@_user_1"), Id: &larkim.UserId{OpenId: strPtr(testInteractiveBotID)}},
		{Key: strPtr("@_user_2"), Id: &larkim.UserId{OpenId: strPtr("ou_other")}, Name: strPtr("李四")},
	}
	content := `{"title":"审批单","elements":[
		[{"tag":"text","text":"请审批这个需求"}],
		[{"tag":"a","href":"https://example.com/spec","text":"需求文档"}],
		[{"tag":"at","user_id":"@_user_1"},{"tag":"at","user_id":"@_user_2"}],
		[{"tag":"button","text":"批准","type":"primary"}],
		[{"tag":"img","image_key":"` + imageKey + `"}]
	]}`
	p.dispatchMessage(
		context.Background(),
		"interactive",
		content,
		mentions,
		messageID,
		"feishu:oc_chat:ou_user",
		"ou_user",
		"oc_chat",
		replyContext{messageID: messageID, sessionKey: "feishu:oc_chat:ou_user"},
		"",
		0,
	)

	select {
	case msg := <-got:
		for _, want := range []string{"审批单", "请审批这个需求", "[需求文档](https://example.com/spec)", "@李四", "批准"} {
			if !strings.Contains(msg.Content, want) {
				t.Fatalf("content %q missing %q", msg.Content, want)
			}
		}
		if strings.Contains(msg.Content, "@_user_1") {
			t.Fatalf("bot mention not stripped: %q", msg.Content)
		}
		if len(msg.Images) != 1 {
			t.Fatalf("images = %d, want 1", len(msg.Images))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("expected interactive message dispatch")
	}
}

// TestDispatchMessageInteractiveRawCard is the regression test for degraded
// Card 2.0 event bodies: the event carries only title + upgrade notice, the
// real content comes from user_card_content. The dispatch must prefer raw.
func TestDispatchMessageInteractiveRawCard(t *testing.T) {
	const appID = "cli_interactive_raw"
	const appSecret = "secret-interactive-raw"
	const messageID = "om_interactive_raw"
	const imageKey = "img_real_1"
	imgBytes := []byte("\x89PNG\r\n\x1a\nfake-png-bytes")

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
		case r.URL.Path == "/open-apis/im/v1/messages/"+messageID &&
			r.URL.Query().Get("card_msg_content_type") == "user_card_content":
			w.Header().Set("Content-Type", "application/json")
			writeJSON(t, w, map[string]any{
				"code": 0,
				"msg":  "success",
				"data": map[string]any{
					"items": []any{
						map[string]any{"body": map[string]any{"content": testCard2RawJSON}},
					},
				},
			})
		case r.URL.Path == "/open-apis/im/v1/messages/"+messageID+"/resources/"+imageKey:
			if _, err := w.Write(imgBytes); err != nil {
				t.Fatalf("write image: %v", err)
			}
		case strings.HasPrefix(r.URL.Path, "/open-apis/contact/v3/users/"):
			w.Header().Set("Content-Type", "application/json")
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success"})
		case strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/chats/"):
			w.Header().Set("Content-Type", "application/json")
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success"})
		default:
			t.Fatalf("unexpected path %s?%s", r.URL.Path, r.URL.RawQuery)
		}
	}))
	defer srv.Close()

	p := &Platform{
		platformName:         "feishu",
		domain:               srv.URL,
		appID:                appID,
		appSecret:            appSecret,
		botOpenID:            testInteractiveBotID,
		resourceDownloadHTTP: srv.Client(),
		client: lark.NewClient(appID, appSecret,
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
		handler: func(_ core.Platform, msg *core.Message) {
			got <- msg
		},
	}

	mentions := []*larkim.MentionEvent{
		{Key: strPtr("@_user_1"), Id: &larkim.UserId{OpenId: strPtr(testInteractiveBotID)}},
		{Key: strPtr("@_user_2"), Id: &larkim.UserId{OpenId: strPtr("ou_owner")}, Name: strPtr("所有者")},
	}
	// Degraded envelope as delivered by the event; real content only via raw.
	content := `{"title":"标题","elements":[[{"tag":"img","image_key":"img_placeholder"}],
		[{"tag":"text","text":"请升级至最新版本客户端，以查看内容"}]]}`
	p.dispatchMessage(
		context.Background(),
		"interactive",
		content,
		mentions,
		messageID,
		"feishu:oc_chat:ou_user",
		"ou_user",
		"oc_chat",
		replyContext{messageID: messageID, sessionKey: "feishu:oc_chat:ou_user"},
		"",
		0,
	)

	select {
	case msg := <-got:
		for _, want := range []string{"测试报告", "## 12%", "通过率", "222", "| 登录 | 通过 |", "@所有者", "查看详情"} {
			if !strings.Contains(msg.Content, want) {
				t.Fatalf("content %q missing %q", msg.Content, want)
			}
		}
		for _, banned := range []string{"请升级至最新版本客户端", "img_placeholder"} {
			if strings.Contains(msg.Content, banned) {
				t.Fatalf("degraded fallback leaked into content %q: %q", banned, msg.Content)
			}
		}
		if len(msg.Images) != 1 {
			t.Fatalf("images = %d, want 1 (raw img, not placeholder)", len(msg.Images))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("expected interactive message dispatch")
	}
}

// TestOnMessage_InteractiveGroupMention verifies the full inbound path for
// cards in group chats: @bot cards reach the handler, unmentioned cards do
// not (same contract as text messages).
func TestOnMessage_InteractiveGroupMention(t *testing.T) {
	newGroupPlatform := func(handler core.MessageHandler) *interactivePlatform {
		platformAny, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret"})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		ip, ok := platformAny.(*interactivePlatform)
		if !ok {
			t.Fatalf("platform type = %T, want *interactivePlatform", platformAny)
		}
		ip.botOpenID = testInteractiveBotID
		ip.handler = handler
		// Fail fast without network: name resolution degrades to bare IDs
		// and the raw-card fetch falls back to the event body.
		ip.client = lark.NewClient("cli_xxx", "secret",
			lark.WithHttpClient(&http.Client{Transport: errTransport{}}))
		return ip
	}

	makeEvent := func(messageID, content string, mentions []*larkim.MentionEvent) *larkim.P2MessageReceiveV1 {
		msgType, chatType, senderType := "interactive", "group", "user"
		openID, chatID := "ou_sender", "oc_group"
		create := strconv.FormatInt(time.Now().UnixMilli(), 10)
		return &larkim.P2MessageReceiveV1{
			Event: &larkim.P2MessageReceiveV1Data{
				Sender: &larkim.EventSender{
					SenderId:   &larkim.UserId{OpenId: &openID},
					SenderType: &senderType,
				},
				Message: &larkim.EventMessage{
					MessageId:   &messageID,
					ChatId:      &chatID,
					ChatType:    &chatType,
					MessageType: &msgType,
					Content:     &content,
					Mentions:    mentions,
					CreateTime:  &create,
				},
			},
		}
	}

	botMention := []*larkim.MentionEvent{
		{Key: strPtr("@_user_1"), Id: &larkim.UserId{OpenId: strPtr(testInteractiveBotID)}},
	}
	cardWithMention := `{"title":"审批单","elements":[[{"tag":"text","text":"请审批"}],[{"tag":"at","user_id":"@_user_1"}]]}`
	cardPlain := `{"title":"周报","elements":[[{"tag":"text","text":"本周进展"}]]}`

	t.Run("mentioned card reaches handler", func(t *testing.T) {
		var wg sync.WaitGroup
		wg.Add(1)
		var received *core.Message
		ip := newGroupPlatform(func(_ core.Platform, msg *core.Message) {
			defer wg.Done()
			received = msg
		})
		if err := ip.onMessage(context.Background(), makeEvent("om_mention_1", cardWithMention, botMention)); err != nil {
			t.Fatalf("onMessage() error = %v", err)
		}
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("expected handler invocation for @bot card")
		}
		if received == nil {
			t.Fatal("expected handler to receive a message")
		}
		if !strings.Contains(received.Content, "请审批") {
			t.Fatalf("content = %q, want card text", received.Content)
		}
		if strings.Contains(received.Content, "@_user_1") {
			t.Fatalf("bot mention not stripped: %q", received.Content)
		}
	})

	t.Run("unmentioned card is dropped", func(t *testing.T) {
		called := make(chan struct{}, 1)
		ip := newGroupPlatform(func(_ core.Platform, _ *core.Message) {
			called <- struct{}{}
		})
		if err := ip.onMessage(context.Background(), makeEvent("om_plain_1", cardPlain, nil)); err != nil {
			t.Fatalf("onMessage() error = %v", err)
		}
		select {
		case <-called:
			t.Fatal("unmentioned group card must not reach handler")
		case <-time.After(500 * time.Millisecond):
		}
	})
}

func TestResolveRawAtIDs_MentionListFirst(t *testing.T) {
	p := &Platform{platformName: "feishu"}
	mentions := []*larkim.MentionEvent{
		{Key: strPtr("@_user_2"), Id: &larkim.UserId{OpenId: strPtr("ou_owner")}, Name: strPtr("所有者")},
	}
	got := p.resolveRawAtIDs("请 @ou_owner 后续跟进", mentions)
	if got != "请 @所有者 后续跟进" {
		t.Fatalf("got %q", got)
	}
}

func TestResolveRawAtIDs_UnknownIDKeptVerbatim(t *testing.T) {
	// No client and no mention entry: the token must survive untouched.
	p := &Platform{platformName: "feishu"}
	in := "抄送 @ou_unknown_1 知悉，邮箱 a@b.com 不受影响"
	if got := p.resolveRawAtIDs(in, nil); got != in {
		t.Fatalf("got %q, want verbatim", got)
	}
}

func TestResolveUserNameAs_TypedLookup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
			writeJSON(t, w, map[string]any{
				"code":                0,
				"msg":                 "success",
				"expire":              7200,
				"tenant_access_token": "tenant-token",
			})
			return
		}
		idType := r.URL.Query().Get("user_id_type")
		switch {
		case strings.HasSuffix(r.URL.Path, "/contact/v3/users/ou_1") && idType == "open_id":
			writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"user": map[string]any{"name": "OuUser"}}})
		case strings.HasSuffix(r.URL.Path, "/contact/v3/users/on_1") && idType == "union_id":
			writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"user": map[string]any{"name": "OnUser"}}})
		default:
			writeJSON(t, w, map[string]any{"code": 1, "msg": "no such user"})
		}
	}))
	defer srv.Close()

	p := &Platform{
		platformName: "feishu",
		client: lark.NewClient("cli_x", "sec_x",
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
	}
	if got := p.resolveUserName("ou_1"); got != "OuUser" {
		t.Fatalf("open_id lookup = %q, want OuUser", got)
	}
	if got := p.resolveUserNameAs("on_1", "union_id"); got != "OnUser" {
		t.Fatalf("union_id lookup = %q, want OnUser", got)
	}
	// Bare-seeded entries keep working for the legacy open_id path.
	p.userNameCache.Store("ou_legacy", "Bare")
	if got := p.resolveUserName("ou_legacy"); got != "Bare" {
		t.Fatalf("legacy cache = %q, want Bare", got)
	}
}

func TestResolveRawAtIDs_UnionFallbackAndEmailGuard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
			writeJSON(t, w, map[string]any{
				"code":                0,
				"msg":                 "success",
				"expire":              7200,
				"tenant_access_token": "tenant-token",
			})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/contact/v3/users/on_9") &&
			r.URL.Query().Get("user_id_type") == "union_id" {
			writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"user": map[string]any{"name": "云用户"}}})
			return
		}
		writeJSON(t, w, map[string]any{"code": 1, "msg": "no such user"})
	}))
	defer srv.Close()

	p := &Platform{
		platformName: "feishu",
		client: lark.NewClient("cli_x", "sec_x",
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
	}
	in := "抄送 @on_9 和 @ou_owner，请联系 a@on_call.com"
	mentions := []*larkim.MentionEvent{
		{Key: strPtr("@_user_2"), Id: &larkim.UserId{OpenId: strPtr("ou_owner")}, Name: strPtr("所有者")},
	}
	want := "抄送 @云用户 和 @所有者，请联系 a@on_call.com"
	if got := p.resolveRawAtIDs(in, mentions); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCardLink_ExtractedFromRawAndEnvelope(t *testing.T) {
	raw := `{"schema":"2.0","header":{"title":{"content":"T"}},
		"card_link":{"url":"https://example.com/card"},
		"body":{"elements":[{"tag":"markdown","content":"hi"}]}}`
	parts, _ := extractCard2RawParts(raw, nil)
	if len(parts) != 3 || parts[2] != "[卡片链接](https://example.com/card)" {
		t.Fatalf("raw card_link parts = %q", parts)
	}

	env := `{"title":"T","card_link":{"pc_url":"https://example.com/pc"},
		"elements":[[{"tag":"text","text":"hi"}]]}`
	parts, _ = extractInteractiveReceiveParts(env)
	if len(parts) != 3 || parts[2] != "[卡片链接](https://example.com/pc)" {
		t.Fatalf("envelope card_link parts = %q", parts)
	}

	// No link configured: nothing appended.
	parts, _ = extractCard2RawParts(`{"schema":"2.0","body":{"elements":[]}}`, nil)
	if len(parts) != 0 {
		t.Fatalf("linkless parts = %q, want empty", parts)
	}
}

func TestCard2Button_DirectURL(t *testing.T) {
	var parts []string
	appendCard2Element(
		json.RawMessage(`{"tag":"button","text":"打开","url":"https://example.com/go"}`),
		&parts, nil, nil)
	if len(parts) != 1 || parts[0] != "[打开](https://example.com/go)" {
		t.Fatalf("parts = %q", parts)
	}
}
