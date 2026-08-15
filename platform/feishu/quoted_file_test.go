package feishu

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/timmyagentic/cc-connect-next/core"
)

func TestFilterQuotedFilesForUserRequiresMentionAndSameSender(t *testing.T) {
	p := &Platform{platformName: "feishu", botOpenID: "ou_bot"}
	metas := []quotedFileMeta{
		{fileKey: "mine", fileName: "mine.txt", messageID: "om_mine", senderID: "ou_alice"},
		{fileKey: "other", fileName: "other.txt", messageID: "om_other", senderID: "ou_bob"},
	}
	mention := []*larkim.MentionEvent{{
		Key: strPtr("@bot"), Id: &larkim.UserId{OpenId: strPtr("ou_bot")}, Name: strPtr("Bot"),
	}}

	if got := p.filterQuotedFilesForUser(metas, nil, "ou_alice"); len(got) != 0 {
		t.Fatalf("quote without bot mention kept %d files", len(got))
	}
	got := p.filterQuotedFilesForUser(metas, mention, "ou_alice")
	if len(got) != 1 || got[0].fileName != "mine.txt" {
		t.Fatalf("same-user filter = %+v, want only mine.txt", got)
	}
}

func TestDispatchMessageDownloadsApprovedQuotedFileOnDemand(t *testing.T) {
	const (
		appID     = "cli_quote"
		appSecret = "secret-quote"
		botID     = "ou_bot"
		userID    = "ou_alice"
		chatID    = "oc_chat"
		parentID  = "om_parent"
		fileKey   = "file_key"
	)
	fileData := []byte("quoted file body")
	resourceCalls := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{
				"code": 0, "msg": "success", "expire": 7200,
				"tenant_access_token": "tenant-token",
			})
		case "/open-apis/im/v1/messages/" + parentID:
			writeJSON(t, w, map[string]any{
				"code": 0, "msg": "success",
				"data": map[string]any{"items": []map[string]any{{
					"msg_type": "file", "parent_id": "",
					"sender": map[string]any{"id": userID, "sender_type": "user"},
					"body":   map[string]any{"content": `{"file_key":"file_key","file_name":"report.txt"}`},
				}}},
			})
		case "/open-apis/im/v1/messages/" + parentID + "/resources/" + fileKey:
			resourceCalls++
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(fileData)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	got := make(chan *core.Message, 2)
	newPlatform := func() *Platform {
		p := &Platform{
			platformName: "feishu", domain: srv.URL, appID: appID, appSecret: appSecret,
			botOpenID: botID,
			client: lark.NewClient(appID, appSecret,
				lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())),
			handler: func(_ core.Platform, msg *core.Message) { got <- msg },
		}
		p.userNameCache.Store(userID, "Alice")
		p.chatNameCache.Store(chatID, "Test chat")
		return p
	}
	mention := []*larkim.MentionEvent{{
		Key: strPtr("@bot"), Id: &larkim.UserId{OpenId: strPtr(botID)}, Name: strPtr("Bot"),
	}}
	rctx := replyContext{messageID: "om_trigger", chatID: chatID, sessionKey: "feishu:" + chatID + ":" + userID}

	p := newPlatform()
	p.dispatchMessage(context.Background(), "text", `{"text":"@bot 分析文件"}`, mention,
		"om_trigger", rctx.sessionKey, userID, chatID, rctx, parentID, 0)
	select {
	case msg := <-got:
		if len(msg.Files) != 1 || msg.Files[0].FileName != "report.txt" || string(msg.Files[0].Data) != string(fileData) {
			t.Fatalf("Files = %+v, want downloaded report.txt", msg.Files)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for quoted-file dispatch")
	}
	if resourceCalls != 1 {
		t.Fatalf("resource calls = %d, want 1", resourceCalls)
	}

	// A rich-text/post reply with the same explicit mention must apply the
	// identical privacy gate and attach the quoted file.
	p = newPlatform()
	p.dispatchMessage(context.Background(), "post", `{"title":"","content":[[{"tag":"text","text":"分析文件"}]]}`, mention,
		"om_trigger_post", rctx.sessionKey, userID, chatID, rctx, parentID, 0)
	select {
	case msg := <-got:
		if len(msg.Files) != 1 || msg.Files[0].FileName != "report.txt" || string(msg.Files[0].Data) != string(fileData) {
			t.Fatalf("post Files = %+v, want downloaded report.txt", msg.Files)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for quoted-file post dispatch")
	}
	if resourceCalls != 2 {
		t.Fatalf("post resource calls = %d, want 2 total", resourceCalls)
	}

	// Quoting the same file without addressing the bot may still fetch the
	// parent metadata, but must not fetch its binary resource.
	p = newPlatform()
	p.dispatchMessage(context.Background(), "text", `{"text":"普通回复"}`, nil,
		"om_trigger_2", rctx.sessionKey, userID, chatID, rctx, parentID, 0)
	select {
	case msg := <-got:
		if len(msg.Files) != 0 {
			t.Fatalf("quote without mention forwarded files: %+v", msg.Files)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for non-mention dispatch")
	}
	if resourceCalls != 2 {
		t.Fatalf("quote without mention made resource call; total = %d", resourceCalls)
	}
}
