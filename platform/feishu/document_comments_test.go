package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkdrive "github.com/larksuite/oapi-sdk-go/v3/service/drive/v1"
)

func documentEvent(t *testing.T) *documentCommentEvent {
	t.Helper()
	var e documentCommentEvent
	err := json.Unmarshal([]byte(`{"event":{"notice_meta":{"file_type":"docx","file_token":"doc1","from_user_id":{"open_id":"ou_author"},"to_user_id":{"open_id":"ou_bot"},"from_user_type":"user","notice_type":"add_comment"},"comment_id":"comment1","reply_id":"reply1","is_mentioned":true}}`), &e)
	if err != nil {
		t.Fatal(err)
	}
	return &e
}

func TestDocumentCommentFiltering(t *testing.T) {
	for _, name := range []string{"disabled", "unmentioned", "other_bot", "self", "denied", "missing_sender", "missing_reply", "unknown_bot", "nil_event"} {
		t.Run(name, func(t *testing.T) {
			p := &Platform{enableDocumentComments: true, botOpenID: "ou_bot", allowFrom: "ou_author"}
			e := documentEvent(t)
			switch name {
			case "disabled":
				p.enableDocumentComments = false
			case "unmentioned":
				e.Event.IsMentioned = new(bool)
			case "other_bot":
				e.Event.NoticeMeta.ToUserId.OpenId = ptrString("ou_other")
			case "self":
				e.Event.NoticeMeta.FromUserId.OpenId = ptrString("ou_bot")
			case "denied":
				p.allowFrom = "ou_other"
			case "missing_sender":
				e.Event.NoticeMeta.FromUserId = nil
			case "missing_reply":
				e.Event.ReplyId = nil
			case "unknown_bot":
				p.botOpenID = ""
			case "nil_event":
				e = nil
			}
			// A nil client ensures filtering happens before any API request.
			if err := p.onDocumentComment(context.Background(), e); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDocumentCommentDispatchAndReply(t *testing.T) {
	var calls atomic.Int32
	writes := make(chan string, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "tenant_access_token") {
			_, _ = io.WriteString(w, `{"code":0,"tenant_access_token":"doc-test-token","expire":7200}`)
			return
		}
		calls.Add(1)
		switch {
		case strings.HasSuffix(r.URL.Path, "/comments/batch_query"):
			_, _ = io.WriteString(w, `{"code":0,"data":{"items":[{"comment_id":"comment1","is_solved":false,"quote":"关键原文"}]}}`)
		case strings.HasSuffix(r.URL.Path, "/replies") && r.Method == "GET":
			if r.URL.Query().Get("page_token") == "" {
				_, _ = io.WriteString(w, `{"code":0,"data":{"items":[{"reply_id":"older","user_id":"ou_other","content":{"elements":[{"type":"text_run","text_run":{"text":"此前讨论"}}]}}],"has_more":true,"page_token":"page2"}}`)
			} else {
				_, _ = fmt.Fprintf(w, `{"code":0,"data":{"items":[{"reply_id":"reply1","user_id":"ou_author","create_time":%d,"content":{"elements":[{"type":"person","person":{"user_id":"ou_bot"}},{"type":"text_run","text_run":{"text":"解释这段 &lt;原文&gt;"}}]}}],"has_more":false}}`, time.Now().Unix())
			}
		case r.URL.Path == "/open-apis/docx/v1/documents/doc1/raw_content":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]string{"content": strings.Repeat("正文", 8000)}})
		case strings.HasSuffix(r.URL.Path, "/replies") && r.Method == "POST":
			if r.URL.Query().Get("file_type") != "docx" {
				t.Error("缺少文档类型")
			}
			var data struct {
				Content *larkdrive.ReplyContent `json:"content"`
			}
			if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
				t.Error(err)
			}
			writes <- stringValue(data.Content.Elements[0].TextRun.Text)
			_, _ = io.WriteString(w, `{"code":0,"data":{"reply_id":"bot_reply","user_id":"ou_bot"}}`)
		default:
			t.Errorf("意外调用 %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	type delivered struct {
		p core.Platform
		m *core.Message
	}
	got := make(chan delivered, 4)
	p := &Platform{platformName: "feishu", appID: "cli_doc_test", botOpenID: "ou_bot", allowFrom: "ou_author", enableDocumentComments: true, documentContext: context.Background(), documentDedup: core.NewMessageDedup(time.Minute), client: lark.NewClient("cli_doc_test", "secret", lark.WithOpenBaseUrl(srv.URL)), handler: func(p core.Platform, m *core.Message) { got <- delivered{p, m} }}
	e := documentEvent(t)
	if err := p.onDocumentComment(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	var d delivered
	select {
	case d = <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("评论未送达 Agent")
	}
	if d.m.SessionKey != "feishu:doc:cli_doc_test:docx:doc1:comment1" || d.m.UserID != "ou_author" {
		t.Fatalf("身份或会话错误: %+v", d.m)
	}
	if !strings.Contains(d.m.Content, "<原文>") || !strings.Contains(d.m.ExtraContent, "此前讨论") || !strings.Contains(d.m.ExtraContent, "关键原文") || !strings.Contains(d.m.ExtraContent, "[truncated]") {
		t.Fatalf("上下文错误: %+v", d.m)
	}
	if _, ok := d.p.(core.CardSender); ok {
		t.Fatal("评论平台不应暴露卡片能力")
	}
	if q, ok := d.p.(core.FinalReplyOnlyPlatform); !ok || !q.FinalReplyOnly() {
		t.Fatal("评论未关闭进度输出")
	}
	count := calls.Load()
	if err := p.onDocumentComment(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != count {
		t.Fatal("重复事件调用了 API")
	}
	if err := d.p.(core.StatusFooterSender).SendWithStatusFooter(context.Background(), d.m.ReplyCtx, "答复 <ok>", "隐藏状态"); err != nil {
		t.Fatal(err)
	}
	if text := <-writes; text != "答复 &lt;ok&gt;" {
		t.Fatalf("回复内容错误: %q", text)
	}
	rc, err := p.ReconstructReplyCtx(d.m.SessionKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Send(context.Background(), rc, strings.Repeat("文", 4001)); err != nil {
		t.Fatal(err)
	}
	if len([]rune(<-writes)) != 4000 || len([]rune(<-writes)) != 1 {
		t.Fatal("中文长回复分块错误")
	}
	if _, ok := p.parseDocumentSessionKey("feishu:doc:other:docx:doc1:comment1"); ok {
		t.Fatal("接受了其他应用的会话")
	}
	other := documentReplyContext{fileType: "docx", fileToken: "doc1", commentID: "comment2"}
	if p.documentSessionKey(other) == d.m.SessionKey {
		t.Fatal("不同评论串未隔离")
	}
}

func TestDocumentCommentReadAndWriteFailures(t *testing.T) {
	for _, kind := range []string{"read_denied", "solved", "author_mismatch", "reply_missing", "write_denied"} {
		t.Run(kind, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.Contains(r.URL.Path, "tenant_access_token"):
					_, _ = io.WriteString(w, `{"code":0,"tenant_access_token":"failure-token","expire":7200}`)
				case r.Method == "POST" && !strings.HasSuffix(r.URL.Path, "/batch_query"):
					_, _ = io.WriteString(w, `{"code":99991672,"msg":"permission denied"}`)
				case kind == "read_denied":
					_, _ = io.WriteString(w, `{"code":99991672,"msg":"permission denied"}`)
				case strings.HasSuffix(r.URL.Path, "/replies"):
					author := "ou_other"
					id := "reply1"
					if kind == "reply_missing" {
						id = "missing"
					}
					_, _ = fmt.Fprintf(w, `{"code":0,"data":{"items":[{"reply_id":%q,"user_id":%q}],"has_more":false}}`, id, author)
				default:
					_, _ = fmt.Fprintf(w, `{"code":0,"data":{"items":[{"comment_id":"comment","is_solved":%t}]}}`, kind == "solved")
				}
			}))
			defer srv.Close()
			p := &Platform{client: lark.NewClient("cli_failure_"+kind, "secret", lark.WithOpenBaseUrl(srv.URL)), handler: func(core.Platform, *core.Message) { t.Error("失败请求进入了 Agent") }}
			rc := documentReplyContext{fileType: "docx", fileToken: "doc", commentID: "comment"}
			var err error
			if kind == "write_denied" {
				err = p.replyDocumentComment(context.Background(), rc, "答复")
			} else {
				err = p.dispatchDocumentComment(context.Background(), rc, "reply1", "ou_author")
			}
			if kind == "solved" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("应返回真实 API 或上下文错误")
			}
		})
	}
}

func TestDocumentWholeCommentUsesNewCommentAPI(t *testing.T) {
	var created atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "tenant_access_token"):
			_, _ = io.WriteString(w, `{"code":0,"tenant_access_token":"whole-token","expire":7200}`)
		case strings.HasSuffix(r.URL.Path, "/batch_query"):
			_, _ = io.WriteString(w, `{"code":0,"data":{"items":[{"comment_id":"whole1","is_whole":true,"is_solved":false}]}}`)
		case strings.HasSuffix(r.URL.Path, "/new_comments"):
			var body struct {
				FileType string `json:"file_type"`
				Elements []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"reply_elements"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.FileType != "docx" || len(body.Elements) == 0 || !strings.Contains(body.Elements[0].Text, "回复评论 whole1") {
				t.Errorf("全文答复缺少关联: %+v", body)
			}
			created.Add(1)
			_, _ = io.WriteString(w, `{"code":0,"data":{"comment_id":"answer1","reply_id":"answer_reply1"}}`)
		default:
			t.Errorf("全文答复不应调用线程或 IM 接口: %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	p := &Platform{client: lark.NewClient("cli_whole_test", "secret", lark.WithOpenBaseUrl(srv.URL))}
	if err := p.replyDocumentComment(context.Background(), documentReplyContext{fileType: "docx", fileToken: "doc", commentID: "whole1"}, "全文答复"); err != nil {
		t.Fatal(err)
	}
	if created.Load() != 1 {
		t.Fatal("未创建全文答复")
	}
}
