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
)

func TestDocumentCommentsAcceptEveryResourceType(t *testing.T) {
	for _, resource := range []string{"docx", "doc", "sheet", "bitable", "slides", "file", "wiki", "future_type"} {
		for _, bodyDenied := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/bodyDenied=%v", resource, bodyDenied), func(t *testing.T) {
				writes := make(chan string, 2)
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					switch {
					case strings.Contains(r.URL.Path, "tenant_access_token"):
						_, _ = io.WriteString(w, `{"code":0,"tenant_access_token":"all-types","expire":7200}`)
					case strings.HasSuffix(r.URL.Path, "/comments/batch_query"):
						if r.URL.Query().Get("file_type") != resource {
							t.Errorf("评论类型被改写成 %s", r.URL.Query().Get("file_type"))
						}
						_, _ = io.WriteString(w, `{"code":0,"data":{"items":[{"comment_id":"comment1","is_solved":false,"is_whole":false,"quote":"H3 目标提交"}]}}`)
					case strings.HasSuffix(r.URL.Path, "/replies") && r.Method == "GET":
						if r.URL.Query().Get("file_type") != resource {
							t.Error("读取回复未保留类型")
						}
						_, _ = io.WriteString(w, `{"code":0,"data":{"items":[{"reply_id":"reply1","user_id":"ou_author","extra":{"notify_extra":{"sheet":"sheet1"}},"content":{"elements":[{"type":"text_run","text_run":{"text":"这个提交改了什么？"}}]}}],"has_more":false}}`)
					case strings.HasSuffix(r.URL.Path, "/replies") && r.Method == "POST":
						writes <- r.URL.Query().Get("file_type")
						_, _ = io.WriteString(w, `{"code":0,"data":{"reply_id":"bot_reply"}}`)
					case strings.Contains(r.URL.Path, "/metas/"):
						_, _ = io.WriteString(w, `{"code":0,"data":{"metas":[{"title":"测试资源","url":"https://example.feishu.cn/resource/token"}]}}`)
					case bodyDenied:
						_, _ = io.WriteString(w, `{"code":99991672,"msg":"permission denied"}`)
					case strings.HasSuffix(r.URL.Path, "/raw_content"):
						_, _ = io.WriteString(w, `{"code":0,"data":{"content":"完整文档正文"}}`)
					case strings.HasSuffix(r.URL.Path, "/sheets/sheet1"):
						_, _ = io.WriteString(w, `{"code":0,"data":{"sheet":{"sheet_id":"sheet1","title":"发布记录","grid_properties":{"row_count":500,"column_count":12}}}}`)
					case strings.Contains(r.URL.Path, "/values/"):
						if !strings.HasSuffix(r.URL.Path, "sheet1!A1:L5") {
							t.Errorf("未读取目标附近单元格: %s", r.URL.Path)
						}
						_, _ = io.WriteString(w, `{"code":0,"data":{"valueRange":{"range":"sheet1!A1:L5","values":[["版本","修复内容"],["V1","真实变更内容"]]}}}`)
					default:
						t.Errorf("非预期接口: %s", r.URL.Path)
						w.WriteHeader(500)
					}
				}))
				defer srv.Close()
				got := make(chan *core.Message, 1)
				p := &Platform{platformName: "feishu", appID: "cli_all_types_" + resource + fmt.Sprint(bodyDenied), botOpenID: "ou_bot", enableDocumentComments: true, client: lark.NewClient("cli_all_types_"+resource+fmt.Sprint(bodyDenied), "secret", lark.WithOpenBaseUrl(srv.URL)), handler: func(_ core.Platform, m *core.Message) { got <- m }}
				e := documentEvent(t)
				e.Event.NoticeMeta.FileType = &resource
				if err := p.onDocumentComment(context.Background(), e); err != nil {
					t.Fatal(err)
				}
				var message *core.Message
				select {
				case message = <-got:
				case <-time.After(3 * time.Second):
					t.Fatal("有效 @被资源类型过滤或正文读取错误阻断")
				}
				if !strings.Contains(message.SessionKey, ":"+resource+":") || !strings.Contains(message.ExtraContent, "H3 目标提交") {
					t.Fatalf("资源类型或引用丢失: %+v", message)
				}
				if resource == "sheet" && !bodyDenied && !strings.Contains(message.ExtraContent, "真实变更内容") {
					t.Fatal("表格单元格正文未传给 Agent")
				}
				if bodyDenied && (resource == "sheet" || resource == "docx") && !strings.Contains(message.ExtraContent, "permission denied") {
					t.Fatal("正文读取失败未如实注入上下文")
				}
				if _, ok := p.parseDocumentSessionKey(message.SessionKey); !ok {
					t.Fatal("无法恢复该资源类型的会话")
				}
				if err := p.Send(context.Background(), message.ReplyCtx, "答复"); err != nil {
					t.Fatal(err)
				}
				if kind := <-writes; kind != resource {
					t.Fatalf("回复写到了错误类型: %s", kind)
				}
			})
		}
	}
}

func TestSheetMissingNotifyExtraDoesNotGuessAmongSheets(t *testing.T) {
	for _, count := range []int{1, 2} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			var reads atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.Contains(r.URL.Path, "tenant_access_token"):
					_, _ = io.WriteString(w, `{"code":0,"tenant_access_token":"sheet-query","expire":7200}`)
				case strings.HasSuffix(r.URL.Path, "/sheets/query"):
					items := []map[string]string{{"sheet_id": "first", "title": "目标表"}}
					if count == 2 {
						items = append(items, map[string]string{"sheet_id": "other", "title": "另一张表"})
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"sheets": items}})
				case strings.Contains(r.URL.Path, "/values/"):
					reads.Add(1)
					_, _ = io.WriteString(w, `{"code":0,"data":{"valueRange":{"values":[["真实数据"]]}}}`)
				default:
					_, _ = io.WriteString(w, `{"code":0,"data":{"sheet":{"title":"目标表","grid_properties":{"row_count":200,"column_count":3}}}}`)
				}
			}))
			defer srv.Close()
			p := &Platform{client: lark.NewClient("cli_single_sheet_"+fmt.Sprint(count), "secret", lark.WithOpenBaseUrl(srv.URL))}
			body, err := p.documentSheetContext(context.Background(), "token", "B2 目标", "")
			if count == 1 {
				if err != nil || !strings.Contains(body, "真实数据") || reads.Load() != 1 {
					t.Fatalf("单工作表应可定位: %s %v", body, err)
				}
			} else {
				if err == nil || reads.Load() != 0 {
					t.Fatal("多工作表未明确定位时不应猜测读取")
				}
			}
		})
	}
}
