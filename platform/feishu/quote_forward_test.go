package feishu

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// A reply to a merged forward must contain both the user's instruction and
// the expanded material, even when the container has no body or isn't first.
// File downloads must retain the existing mention + same-sender privacy gate.
func TestDispatchMessageQuotedMergeForward_ImagesUseContainerID(t *testing.T) {
	for _, tc := range []struct {
		name       string
		rootLast   bool
		emptyBody  bool
		mentionBot bool
		fileSender string
		wantFiles  int
		direct     bool
	}{
		{name: "root first", fileSender: "ou_sender"},
		{name: "empty container", emptyBody: true, fileSender: "ou_sender"},
		{name: "root after children", rootLast: true, fileSender: "ou_sender"},
		{name: "own file with mention", mentionBot: true, fileSender: "ou_sender", wantFiles: 1},
		{name: "foreign file with mention", mentionBot: true, fileSender: "ou_foreign"},
		{name: "direct forward retains file download", fileSender: "ou_foreign", wantFiles: 1, direct: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := quotedForwardItem("om_forward", "", "merge_forward", "Merged and Forwarded Message", "ou_sender")
			if tc.emptyBody {
				delete(root, "body")
			}
			items := []any{
				quotedForwardItem("om_text", "om_forward", "text", `{"text":"first material"}`, "ou_sender"),
				quotedForwardItem("om_nested", "om_forward", "merge_forward", "", "ou_sender"),
				quotedForwardItem("om_nested_text", "om_nested", "text", `{"text":"nested material"}`, "ou_sender"),
				quotedForwardItem("om_image", "om_forward", "image", `{"image_key":"img_test"}`, "ou_sender"),
				quotedForwardItem("om_nested_image", "om_nested", "image", `{"image_key":"img_nested"}`, "ou_sender"),
				quotedForwardItem("om_post", "om_nested", "post", `{"title":"post material","content":[[{"tag":"img","image_key":"img_post"}]]}`, "ou_sender"),
				quotedForwardItem("om_file", "om_nested", "file", `{"file_key":"file_test","file_name":"report.pdf"}`, tc.fileSender),
			}
			deleted := quotedForwardItem("om_deleted", "om_forward", "text", `{"text":"deleted material"}`, "ou_sender")
			deleted["deleted"] = true
			items = append(items, deleted, nil)
			if tc.rootLast {
				items = append(items, root)
			} else {
				items = append([]any{root}, items...)
			}
			var lookupCalls, fileCalls atomic.Int32
			imageData := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/open-apis/auth/v3/tenant_access_token/internal":
					writeJSON(t, w, map[string]any{"code": 0, "tenant_access_token": "test-token", "expire": 7200})
				case "/open-apis/im/v1/messages/om_forward":
					lookupCalls.Add(1)
					writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"items": items}})
				case "/open-apis/im/v1/messages/om_forward/resources/img_test",
					"/open-apis/im/v1/messages/om_forward/resources/img_nested",
					"/open-apis/im/v1/messages/om_forward/resources/img_post":
					w.Header().Set("Content-Type", "image/png")
					_, _ = w.Write(imageData)
				case "/open-apis/im/v1/messages/om_file/resources/file_test":
					fileCalls.Add(1)
					w.Header().Set("Content-Type", "application/pdf")
					_, _ = w.Write([]byte("%PDF-1.4 test"))
				case "/open-apis/im/v1/messages/om_image/resources/img_test",
					"/open-apis/im/v1/messages/om_nested_image/resources/img_nested",
					"/open-apis/im/v1/messages/om_post/resources/img_post":
					w.WriteHeader(http.StatusBadRequest)
					writeJSON(t, w, map[string]any{"code": 234003, "msg": "File not in msg."})
				default:
					t.Errorf("unexpected API request: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()
			var received []*core.Message
			p := &Platform{
				platformName: "feishu", botOpenID: "ou_bot",
				domain: srv.URL, appID: "quote-forward", appSecret: "test-secret",
				resourceDownloadHTTP: srv.Client(),
				client:               lark.NewClient("quote-forward", "test-secret", lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())),
				handler:              func(_ core.Platform, msg *core.Message) { received = append(received, msg) },
			}
			p.userNameCache.Store("ou_sender", "Sender")
			p.userNameCache.Store("ou_foreign", "Other user")
			p.chatNameCache.Store("oc_chat", "Chat")
			var mentions []*larkim.MentionEvent
			if tc.mentionBot {
				mentions = []*larkim.MentionEvent{{Key: strPtr("@_user_1"), Id: &larkim.UserId{OpenId: strPtr("ou_bot")}}}
			}
			if tc.direct {
				p.dispatchMessage(context.Background(), "merge_forward", "", nil,
					"om_forward", "feishu:oc_chat:ou_sender", "ou_sender", "oc_chat",
					replyContext{messageID: "om_forward"}, "", 1234)
			} else {
				p.dispatchMessage(context.Background(), "text", `{"text":"summarize the disagreement"}`, mentions,
					"om_reply", "feishu:oc_chat:ou_sender", "ou_sender", "oc_chat",
					replyContext{messageID: "om_reply"}, "om_forward", 1234)
			}
			if len(received) != 1 {
				t.Fatalf("received %d messages, want one combined input", len(received))
			}
			msg := received[0]
			if !tc.direct && (msg.Content != "summarize the disagreement" || msg.MessageID != "om_reply" || msg.UserMessageTimeMs != 1234) {
				t.Fatalf("reply instruction/identity changed: %+v", msg)
			}
			material := msg.ExtraContent
			if tc.direct {
				material = msg.Content
			}
			for _, want := range []string{"first material", "nested material", "<forwarded_messages>", "report.pdf"} {
				if !strings.Contains(material, want) {
					t.Errorf("material missing %q: %s", want, material)
				}
			}
			if strings.Contains(material, "deleted material") {
				t.Error("deleted forward child must not be included")
			}
			if len(msg.Images) != 3 {
				t.Errorf("quoted forward lost image")
			}
			for _, img := range msg.Images {
				if string(img.Data) != string(imageData) {
					t.Error("forward image bytes changed")
				}
			}
			if len(msg.Files) != tc.wantFiles || int(fileCalls.Load()) != tc.wantFiles {
				t.Errorf("files = %d, downloads = %d, want %d", len(msg.Files), fileCalls.Load(), tc.wantFiles)
			}
			if lookupCalls.Load() != 1 {
				t.Errorf("forward fetched %d times, want once", lookupCalls.Load())
			}
		})
	}
}

func TestForwardImageFix_OrdinaryImagesKeepOwnMessageID(t *testing.T) {
	imageData := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{"code": 0, "tenant_access_token": "test-token", "expire": 7200})
		case "/open-apis/im/v1/messages/om_ordinary/resources/img_test",
			"/open-apis/im/v1/messages/om_post/resources/img_post":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(imageData)
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	p := &Platform{domain: srv.URL, appID: "ordinary-images", appSecret: "test-secret", resourceDownloadHTTP: srv.Client()}
	data, _, err := p.downloadImage("om_ordinary", "img_test")
	if err != nil || string(data) != string(imageData) {
		t.Fatalf("ordinary image: error=%v, data=%x", err, data)
	}
	_, images := p.parsePostContent("om_post", `{"content":[[{"tag":"img","image_key":"img_post"}]]}`)
	if len(images) != 1 || string(images[0].Data) != string(imageData) {
		t.Fatal("ordinary post image lost")
	}
}

func TestFetchQuotedMergeForwardRejectsMissingOrDeletedRoot(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing root", true: "deleted root"}[deleted], func(t *testing.T) {
			items := []any{quotedForwardItem("om_child", "om_forward", "text", `{"text":"must not substitute child for root"}`, "")}
			if deleted {
				root := quotedForwardItem("om_forward", "", "merge_forward", "", "")
				root["deleted"] = true
				items = append(items, root)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/tenant_access_token/internal") {
					writeJSON(t, w, map[string]any{"code": 0, "tenant_access_token": "test-token", "expire": 7200})
					return
				}
				writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"items": items}})
			}))
			defer srv.Close()
			p := &Platform{client: lark.NewClient("quote-forward-missing", "test-secret", lark.WithOpenBaseUrl(srv.URL))}
			if got := p.fetchQuotedMessage(context.Background(), "om_forward"); got.text != "" {
				t.Fatalf("unexpected quote: %s", got.text)
			}
		})
	}
}

func quotedForwardItem(id, upper, kind, content, sender string) map[string]any {
	return map[string]any{
		"message_id": id, "upper_message_id": upper, "msg_type": kind,
		"body":   map[string]any{"content": content},
		"sender": map[string]any{"id": sender, "sender_type": "user"},
	}
}
