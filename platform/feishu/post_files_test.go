package feishu

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// mustParsePost decodes a post body for tests and fails when it is unparseable.
func mustParsePost(t *testing.T, p *Platform, raw string) *postLang {
	t.Helper()
	post := p.parsePostBody(raw)
	if post == nil {
		t.Fatalf("parsePostBody(%q) = nil", raw)
	}
	return post
}

func TestDispatchPostPreservesFileAttachments(t *testing.T) {
	cases := []struct {
		name, body string
		want       int
		text       string
		calls      int
	}{
		{"text and CSV", `{"content":[[{"tag":"text","text":"analyze"}]],"files":[{"file_key":"file_a","file_name":"名单.csv","is_folder":false}]}`, 1, "analyze", 1},
		{"file only", `{"files":[{"file_key":"file_a","file_name":"a.csv"}]}`, 1, "", 1},
		{"locale wrapper", `{"zh_cn":{"content":[],"files":[{"file_key":"file_a","file_name":"a.csv"}]}}`, 1, "", 1},
		{"multiple", `{"content":[],"files":[{"file_key":"file_a","file_name":"a.csv"},{"file_key":"file_b","file_name":"b.xlsx"}]}`, 2, "", 2},
		{"text only", `{"content":[[{"tag":"text","text":"hello"}]]}`, 0, "hello", 0},
		// A failed Range probe falls back to one plain GET, so a missing file costs 2 requests.
		{"partial failure", `{"content":[[{"tag":"text","text":"analyze"}]],"files":[{"file_key":"bad","file_name":"failed.csv"},{"file_key":"file_a","file_name":"a.csv"}]}`, 1, "failed.csv", 3},
		{"failure only", `{"files":[{"file_key":"bad","file_name":"failed.csv"}]}`, 0, "failed.csv", 2},
		{"missing key", `{"files":[{"file_name":"missing.csv"}]}`, 0, "missing.csv", 0},
		{"folder", `{"files":[{"file_key":"folder","file_name":"folder.zip","is_folder":true}]}`, 0, "folder.zip", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/resources/") {
					calls.Add(1)
					if r.URL.Query().Get("type") != "file" {
						t.Errorf("resource type = %q", r.URL.Query().Get("type"))
					}
					if !strings.Contains(r.URL.Path, "/messages/om_post/") {
						t.Errorf("wrong parent message: %s", r.URL.Path)
					}
					if strings.HasSuffix(r.URL.Path, "/bad") {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(404)
						_, _ = fmt.Fprint(w, `{"code":234001,"msg":"missing"}`)
						return
					}
					w.Header().Set("Content-Type", "application/octet-stream")
					_, _ = fmt.Fprint(w, "column\nvalue\n")
					return
				}
				t.Errorf("unexpected request: %s", r.URL.Path)
				w.WriteHeader(404)
			}))
			t.Cleanup(srv.Close)
			var got *core.Message
			p := &Platform{
				platformName:         "feishu",
				domain:               srv.URL,
				resourceDownloadHTTP: srv.Client(),
				fetchResourceToken:   func(_ context.Context) (string, error) { return "test-token", nil },
				handler:              func(_ core.Platform, m *core.Message) { got = m },
			}
			p.dispatchMessage(context.Background(), "post", tc.body, nil, "om_post", "session", "", "", replyContext{}, "", 0)
			if got == nil {
				t.Fatal("post was silently dropped")
			}
			if len(got.Files) != tc.want {
				t.Fatalf("files=%d, want %d", len(got.Files), tc.want)
			}
			if tc.text != "" && !strings.Contains(got.Content, tc.text) {
				t.Errorf("content=%q, missing %q", got.Content, tc.text)
			}
			for _, f := range got.Files {
				if string(f.Data) != "column\nvalue\n" || f.FileName == "" {
					t.Errorf("attachment bytes/name lost: %q", f.FileName)
				}
				if strings.HasPrefix(f.MimeType, "image/") {
					t.Errorf("file mislabeled: %s", f.MimeType)
				}
			}
			if calls.Load() != int32(tc.calls) {
				t.Errorf("downloads=%d, want %d", calls.Load(), tc.calls)
			}
		})
	}
}

// A post body carrying several locales must hand the agent the text and the
// files of the same locale; two independent map walks used to disagree ~25%.
func TestParsePostBodyPicksOneLocaleForTextAndFiles(t *testing.T) {
	p := &Platform{platformName: "feishu"}
	raw := `{"zh_cn":{"content":[[{"tag":"text","text":"中文"}]],"files":[{"file_key":"zh","file_name":"zh.csv"}]},` +
		`"en_us":{"content":[[{"tag":"text","text":"english"}]],"files":[{"file_key":"en","file_name":"en.csv"}]}}`
	for i := 0; i < 200; i++ {
		post := mustParsePost(t, p, raw)
		texts, _ := p.extractPostParts("", post)
		files := post.Files
		if len(texts) != 1 || len(files) != 1 {
			t.Fatalf("texts=%v files=%v", texts, files)
		}
		if texts[0] != "english" || files[0].FileName != "en.csv" {
			t.Fatalf("locale skew: text=%q file=%q", texts[0], files[0].FileName)
		}
	}
}

// A merge-forward bundle may nest posts with attachments: successful files reach
// the agent next to their message, failed ones become a visible notice, and
// no attachment is labelled as an image.
func TestFormatMergeForwardTreeSurfacesPostFiles(t *testing.T) {
	pinFeishuDisplayLocation(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/resources/") {
			t.Errorf("unexpected request: %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/messages/om_root/resources/") {
			t.Errorf("forwarded post file used wrong container: %s", r.URL.Path)
		}
		calls.Add(1)
		if strings.HasSuffix(r.URL.Path, "/bad") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(404)
			_, _ = fmt.Fprint(w, `{"code":234001,"msg":"missing"}`)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = fmt.Fprint(w, "column\nvalue\n")
	}))
	t.Cleanup(srv.Close)
	item := func(id, msgType, content string) *larkim.Message {
		return &larkim.Message{
			MessageId:  strPtr(id),
			MsgType:    strPtr(msgType),
			CreateTime: strPtr("1792027571969"),
			Sender:     &larkim.Sender{Id: strPtr("ou_alice")},
			Body:       &larkim.MessageBody{Content: strPtr(content)},
		}
	}
	children := map[string][]*larkim.Message{
		"om_root": {
			item("om_1", "post", `{"content":[[{"tag":"text","text":"first"}]],"files":[{"file_key":"file_a","file_name":"a.csv"}]}`),
			item("om_2", "post", `{"content":[[{"tag":"text","text":"second"}]],"files":[{"file_key":"bad","file_name":"failed.csv"}]}`),
			item("om_3", "merge_forward", ""),
		},
		"om_3": {
			item("om_4", "file", `{"file_key":"file_b","file_name":"b.xlsx"}`),
			item("om_5", "post", `{"files":[{"file_key":"file_c","file_name":"nested.csv"}]}`),
		},
	}
	p := &Platform{
		platformName:         "feishu",
		domain:               srv.URL,
		resourceDownloadHTTP: srv.Client(),
		fetchResourceToken:   func(_ context.Context) (string, error) { return "test-token", nil },
	}
	var sb strings.Builder
	var files []core.FileAttachment
	p.formatMergeForwardTree("om_root", "om_root", children, map[string]string{"ou_alice": "Alice"}, &sb, new([]core.ImageAttachment), &files, 0)
	out := sb.String()
	if len(files) != 3 || files[0].FileName != "a.csv" || files[1].FileName != "b.xlsx" || files[2].FileName != "nested.csv" {
		t.Fatalf("files = %+v, want a.csv, b.xlsx, nested.csv", files)
	}
	for _, f := range files {
		if string(f.Data) != "column\nvalue\n" || strings.HasPrefix(f.MimeType, "image/") {
			t.Errorf("attachment %s: bytes lost or mislabeled as %s", f.FileName, f.MimeType)
		}
	}
	for _, want := range []string{"first", "second", "failed.csv", "[file: b.xlsx]"} {
		if !strings.Contains(out, want) {
			t.Errorf("forwarded bundle = %q, want it to contain %q", out, want)
		}
	}
	// a.csv, b.xlsx and nested.csv cost one request each; the missing file costs a Range probe plus a plain GET.
	if calls.Load() != 5 {
		t.Errorf("downloads=%d, want 5", calls.Load())
	}
}

// Quoted attachments go through downloadQuotedFiles and must carry the same
// MIME label as the direct post path.
func TestDownloadQuotedFilesUsesContentSniffing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = fmt.Fprint(w, "column\nvalue\n")
	}))
	t.Cleanup(srv.Close)
	p := &Platform{
		platformName:         "feishu",
		domain:               srv.URL,
		resourceDownloadHTTP: srv.Client(),
		fetchResourceToken:   func(_ context.Context) (string, error) { return "test-token", nil },
	}
	got := p.downloadQuotedFiles(context.Background(), []quotedFileMeta{{fileKey: "file_a", fileName: "a.csv", messageID: "om_parent", senderID: "ou_owner"}})
	if len(got) != 1 || strings.HasPrefix(got[0].MimeType, "image/") {
		t.Fatalf("quoted file = %+v, want one non-image attachment", got)
	}
}

// A file sent on its own must be typed the same way as one attached to a post.
func TestDispatchFileMessageUsesContentSniffing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = fmt.Fprint(w, "column\nvalue\n")
	}))
	t.Cleanup(srv.Close)
	var got *core.Message
	p := &Platform{
		platformName:         "feishu",
		domain:               srv.URL,
		resourceDownloadHTTP: srv.Client(),
		fetchResourceToken:   func(_ context.Context) (string, error) { return "test-token", nil },
		handler:              func(_ core.Platform, m *core.Message) { got = m },
	}
	p.dispatchMessage(context.Background(), "file", `{"file_key":"file_a","file_name":"a.csv"}`, nil, "om_file", "session", "", "", replyContext{}, "", 0)
	if got == nil || len(got.Files) != 1 {
		t.Fatalf("file message not dispatched: %+v", got)
	}
	if mt := got.Files[0].MimeType; strings.HasPrefix(mt, "image/") {
		t.Errorf("CSV mislabeled as %s", mt)
	}
}
