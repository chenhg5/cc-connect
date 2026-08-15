package feishu

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestValidatePlatformOptionsMentionMap(t *testing.T) {
	validate := validatePlatformOptions("feishu")

	tests := []struct {
		name    string
		opts    map[string]any
		wantErr string
	}{
		{
			name: "valid",
			opts: map[string]any{
				"app_id":           "cli_test",
				"app_secret":       "secret",
				"resolve_mentions": true,
				"mention_map":      map[string]any{"Reviewer-Bot": "ou_reviewer"},
			},
		},
		{
			name: "requires resolve mentions",
			opts: map[string]any{
				"app_id":      "cli_test",
				"app_secret":  "secret",
				"mention_map": map[string]any{"Reviewer-Bot": "ou_reviewer"},
			},
			wantErr: "requires resolve_mentions = true",
		},
		{
			name: "rejects app id",
			opts: map[string]any{
				"app_id":           "cli_test",
				"app_secret":       "secret",
				"resolve_mentions": true,
				"mention_map":      map[string]any{"Reviewer-Bot": "cli_wrong_identifier"},
			},
			wantErr: "must be a bot open_id starting with ou_",
		},
		{
			name: "rejects non string",
			opts: map[string]any{
				"app_id":           "cli_test",
				"app_secret":       "secret",
				"resolve_mentions": true,
				"mention_map":      map[string]any{"Reviewer-Bot": 42},
			},
			wantErr: "must be a string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validate(tt.opts)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validatePlatformOptions() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validatePlatformOptions() error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestResolveMentionsMentionMapOverridesMembersAndForcesText(t *testing.T) {
	p := &Platform{
		platformName:    "feishu",
		resolveMentions: true,
		mentionMap:      map[string]string{"Reviewer-Bot": "ou_bot"},
	}
	p.chatMemberCache.Store("oc_chat", &chatMemberEntry{
		members:   map[string]string{"Reviewer-Bot": "ou_human", "Reviewer": "ou_reviewer"},
		fetchedAt: time.Now(),
	})

	resolved := p.resolveMentionsInContent(context.Background(), "oc_chat", "**完成**，请 @Reviewer-Bot 和 @Reviewer 复核")
	if !strings.Contains(resolved, `<at user_id="ou_bot">Reviewer-Bot</at>`) {
		t.Fatalf("mention_map target was not resolved: %q", resolved)
	}
	if strings.Contains(resolved, "ou_human") {
		t.Fatalf("group member unexpectedly overrode mention_map: %q", resolved)
	}
	if !strings.Contains(resolved, `<at user_id="ou_reviewer">Reviewer</at>`) {
		t.Fatalf("ordinary member target was not resolved: %q", resolved)
	}
	msgType, _ := buildReplyContentWithResolvedMention(resolved, true)
	if msgType != larkim.MsgTypeText {
		t.Fatalf("resolved mentions must use text delivery for native notification, got %q", msgType)
	}
}

func TestLegacyReplyTransportRequiresResolverProducedMention(t *testing.T) {
	base := &Platform{
		platformName:    "feishu",
		resolveMentions: true,
		mentionMap:      map[string]string{"Reviewer-Bot": "ou_bot"},
	}
	raw := `<at user_id="ou_untrusted">Example</at>`
	for _, content := range []string{raw, "**示例** " + raw} {
		msgType, _ := buildReplyContent(content)
		if msgType == larkim.MsgTypeText {
			t.Fatalf("literal native markup forced text transport: %q", content)
		}
	}

	mixed := raw + "；请 @Reviewer-Bot 复核"
	prepared, resolved := base.prepareOutboundMentions(context.Background(), "oc_chat", mixed)
	if resolved || prepared != mixed {
		t.Fatalf("literal markup plus alias must fail closed: (%q, %v)", prepared, resolved)
	}
	msgType, _ := buildReplyContentWithResolvedMention(prepared, resolved)
	if msgType == larkim.MsgTypeText {
		t.Fatal("literal markup plus alias forced text transport")
	}
}

func TestRichCardTerminalTextFallbackOnlyWhenMentionResolved(t *testing.T) {
	base := &Platform{
		platformName:    "feishu",
		resolveMentions: true,
		mentionMap:      map[string]string{"Reviewer-Bot": "ou_bot"},
	}
	p := &interactivePlatform{Platform: base}

	plain, required, err := p.PrepareRichCardTerminalText(context.Background(), replyContext{chatID: "oc_chat"}, "普通回答")
	if err != nil || required || plain != "普通回答" {
		t.Fatalf("plain terminal preparation = (%q, %v, %v)", plain, required, err)
	}

	resolved, required, err := p.PrepareRichCardTerminalText(context.Background(), replyContext{chatID: "oc_chat"}, "请 @Reviewer-Bot 复核")
	if err != nil {
		t.Fatalf("PrepareRichCardTerminalText() error = %v", err)
	}
	if !required || !strings.Contains(resolved, `<at user_id="ou_bot">Reviewer-Bot</at>`) {
		t.Fatalf("mention terminal preparation = (%q, %v)", resolved, required)
	}

	rawMarkup := `文档示例：<at user_id="ou_untrusted">Example</at>`
	prepared, required, err := p.PrepareRichCardTerminalText(context.Background(), replyContext{chatID: "oc_chat"}, rawMarkup)
	if err != nil || required || prepared != rawMarkup {
		t.Fatalf("literal at-markup must not trigger terminal text fallback: (%q, %v, %v)", prepared, required, err)
	}
	rawMarkupWithAlias := rawMarkup + "；请 @Reviewer-Bot 复核"
	prepared, required, err = p.PrepareRichCardTerminalText(context.Background(), replyContext{chatID: "oc_chat"}, rawMarkupWithAlias)
	if err != nil || required || prepared != rawMarkupWithAlias {
		t.Fatalf("raw at-markup must fail closed even beside an alias: (%q, %v, %v)", prepared, required, err)
	}

	codeOnly := "内联示例 `@Reviewer-Bot`\n```text\n@Reviewer-Bot\n```"
	prepared, required, err = p.PrepareRichCardTerminalText(context.Background(), replyContext{chatID: "oc_chat"}, codeOnly)
	if err != nil || required || prepared != codeOnly {
		t.Fatalf("mentions in code must remain literal: (%q, %v, %v)", prepared, required, err)
	}
	escaped := `文档写法：\@Reviewer-Bot`
	prepared, required, err = p.PrepareRichCardTerminalText(context.Background(), replyContext{chatID: "oc_chat"}, escaped)
	if err != nil || required || prepared != escaped {
		t.Fatalf("escaped mention must remain literal: (%q, %v, %v)", prepared, required, err)
	}
	for _, identifier := range []string{"support@Reviewer-Bot.com", "alice@Reviewer-Bot"} {
		prepared, required, err = p.PrepareRichCardTerminalText(context.Background(), replyContext{chatID: "oc_chat"}, identifier)
		if err != nil || required || prepared != identifier {
			t.Fatalf("identifier-embedded alias must remain literal: (%q, %v, %v)", prepared, required, err)
		}
	}
	for _, link := range []string{
		"[profile](https://host/@Reviewer-Bot)",
		`[profile](https://host/profile "hidden ) @Reviewer-Bot title")`,
		"[profile]: /users/@Reviewer-Bot",
		"[@Reviewer-Bot]: /users/profile",
		"[profile][@Reviewer-Bot]",
		"![avatar @Reviewer-Bot](https://host/avatar.png)",
		"![avatar][@Reviewer-Bot]",
		"[^@Reviewer-Bot]",
		`<span data-user="@Reviewer-Bot">profile</span>`,
		"<!-- hidden @Reviewer-Bot -->",
		"<code>@Reviewer-Bot</code>",
		"<pre><span>@Reviewer-Bot</span></pre>",
		"[profile]: <../users/@Reviewer-Bot> \"hidden title\"",
		"[profile]:\n  ../users/@Reviewer-Bot",
		"[profile]: /users/profile\n  \"hidden @Reviewer-Bot title\"",
		"<https://host/@Reviewer-Bot>",
		"https://host/@Reviewer-Bot",
		"<mailto:support@Reviewer-Bot.example>",
	} {
		prepared, required, err = p.PrepareRichCardTerminalText(context.Background(), replyContext{chatID: "oc_chat"}, link)
		if err != nil || required || prepared != link {
			t.Fatalf("link destination alias must remain literal: (%q, %v, %v)", prepared, required, err)
		}
	}
	visibleLabel := "[@Reviewer-Bot](https://host/profile)"
	prepared, required, err = p.PrepareRichCardTerminalText(context.Background(), replyContext{chatID: "oc_chat"}, visibleLabel)
	if err != nil || !required || !strings.Contains(prepared, `<at user_id="ou_bot">Reviewer-Bot</at>`) || !strings.Contains(prepared, "https://host/profile") {
		t.Fatalf("visible link-label mention must resolve without changing its destination: (%q, %v, %v)", prepared, required, err)
	}
	visibleReferenceLabel := "[@Reviewer-Bot][profile]"
	prepared, required, err = p.PrepareRichCardTerminalText(context.Background(), replyContext{chatID: "oc_chat"}, visibleReferenceLabel)
	if err != nil || !required || !strings.Contains(prepared, `<at user_id="ou_bot">Reviewer-Bot</at>`) || !strings.Contains(prepared, "[profile]") {
		t.Fatalf("visible reference-link label must resolve without changing its identifier: (%q, %v, %v)", prepared, required, err)
	}
	visibleHTMLText := "<span>@Reviewer-Bot</span>"
	prepared, required, err = p.PrepareRichCardTerminalText(context.Background(), replyContext{chatID: "oc_chat"}, visibleHTMLText)
	if err != nil || !required || strings.Count(prepared, `<at user_id="ou_bot">Reviewer-Bot</at>`) != 1 {
		t.Fatalf("visible HTML text mention must resolve outside tag attributes: (%q, %v, %v)", prepared, required, err)
	}

	mixed := "[profile]: /users/@Reviewer-Bot\n示例 `@Reviewer-Bot` 和 [链接](https://host/@Reviewer-Bot)；真正通知 @Reviewer-Bot。"
	prepared, required, err = p.PrepareRichCardTerminalText(context.Background(), replyContext{chatID: "oc_chat"}, mixed)
	if err != nil || !required {
		t.Fatalf("mixed terminal preparation = (%q, %v, %v)", prepared, required, err)
	}
	if !strings.Contains(prepared, "`@Reviewer-Bot`") || !strings.Contains(prepared, "https://host/@Reviewer-Bot") || !strings.Contains(prepared, "/users/@Reviewer-Bot") || strings.Count(prepared, `<at user_id="ou_bot">Reviewer-Bot</at>`) != 1 {
		t.Fatalf("only the real mention should be resolved: %q", prepared)
	}
	if partial := p.TransformRichCardMarkdown(context.Background(), replyContext{chatID: "oc_chat"}, "@Reviewer-BotExtra"); partial != "@Reviewer-BotExtra" {
		t.Fatalf("partial alias unexpectedly resolved: %q", partial)
	}
}

func TestSendRichCardTerminalTextReturnsRecallableHandle(t *testing.T) {
	const chatID = "oc_chat"
	var gotMsgType string
	var gotContent string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{"code": 0, "expire": 7200, "tenant_access_token": "t"})
		case r.URL.Path == "/open-apis/im/v1/messages" && r.Method == http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			var req struct {
				MsgType string `json:"msg_type"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatalf("unmarshal request: %v", err)
			}
			gotMsgType = req.MsgType
			gotContent = req.Content
			writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"message_id": "om_text_final"}})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	base := &Platform{
		platformName: "feishu",
		domain:       srv.URL,
		appID:        "cli_test",
		appSecret:    "secret",
		client: lark.NewClient("cli_test", "secret",
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client())),
		replayClient: lark.NewClient("cli_test", "secret",
			lark.WithEnableTokenCache(false),
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client())),
	}
	p := &interactivePlatform{Platform: base}
	handle, err := p.SendRichCardTerminalText(
		context.Background(),
		replyContext{chatID: chatID},
		`完成，请 <at user_id="ou_bot">Reviewer-Bot</at> 复核`,
	)
	if err != nil {
		t.Fatalf("SendRichCardTerminalText() error = %v", err)
	}
	h, ok := handle.(*feishuPreviewHandle)
	if !ok || h.messageID != "om_text_final" || h.chatID != chatID {
		t.Fatalf("terminal handle = %#v", handle)
	}
	if gotMsgType != larkim.MsgTypeText || !strings.Contains(gotContent, "ou_bot") {
		t.Fatalf("sent message = type %q content %q", gotMsgType, gotContent)
	}
}
