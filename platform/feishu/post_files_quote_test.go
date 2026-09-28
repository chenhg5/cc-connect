package feishu

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestQuotedPostFilesStayLazyAndRetainSender(t *testing.T) {
	var downloads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "tenant_access_token"):
			fmt.Fprint(w, `{"code":0,"tenant_access_token":"token","expire":7200}`)
		case strings.Contains(r.URL.Path, "/resources/"):
			downloads.Add(1)
			t.Error("quoted metadata lookup eagerly downloaded a file")
		case strings.Contains(r.URL.Path, "/contact/"):
			fmt.Fprint(w, `{"code":0,"data":{"user":{"name":"Owner"}}}`)
		default:
			writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"items": []any{map[string]any{"msg_type": "post", "sender": map[string]any{"id": "ou_owner", "sender_type": "user"}, "body": map[string]any{"content": `{"files":[{"file_key":"file_a","file_name":"a.csv"}]}`}}}}})
		}
	}))
	t.Cleanup(srv.Close)
	p := &Platform{platformName: "feishu", domain: srv.URL, botOpenID: "ou_bot", client: lark.NewClient("lazy_post_files", "secret", lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client()))}
	msg := p.fetchSingleMessage(context.Background(), "om_parent")
	if msg == nil || len(msg.files) != 1 {
		t.Fatal("files-only quoted post was dropped")
	}
	f := msg.files[0]
	if f.messageID != "om_parent" || f.senderID != "ou_owner" || f.fileKey != "file_a" {
		t.Fatal("quoted file identity lost")
	}
	if downloads.Load() != 0 {
		t.Fatal("quoted files fetched eagerly")
	}
	mentions := []*larkim.MentionEvent{{Id: &larkim.UserId{OpenId: strPtr("ou_bot")}}}
	if len(p.filterQuotedFilesForUser(msg.files, nil, "ou_owner")) != 0 {
		t.Fatal("missing mention allowed")
	}
	if len(p.filterQuotedFilesForUser(msg.files, mentions, "ou_other")) != 0 {
		t.Fatal("foreign sender allowed")
	}
	if len(p.filterQuotedFilesForUser(msg.files, mentions, "ou_owner")) != 1 {
		t.Fatal("same sender mention rejected")
	}
}
