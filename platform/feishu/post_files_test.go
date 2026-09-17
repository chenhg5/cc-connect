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
)

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
		{"multiple and duplicate", `{"content":[],"files":[{"file_key":"file_a","file_name":"a.csv"},{"file_key":"file_b","file_name":"b.xlsx"},{"file_key":"file_a","file_name":"a.csv"}]}`, 2, "", 2},
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
						fmt.Fprint(w, `{"code":234001,"msg":"missing"}`)
						return
					}
					w.Header().Set("Content-Type", "application/octet-stream")
					fmt.Fprint(w, "column\nvalue\n")
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
