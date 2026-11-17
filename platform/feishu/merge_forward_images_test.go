package feishu

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
)

// Feishu rejects forwarded image resources requested through a child message
// with 234003, including images embedded in posts and nested forwards.
func TestMergeForwardImagesUseOuterContainer(t *testing.T) {
	var imageBody bytes.Buffer
	if err := png.Encode(&imageBody, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	imageData := imageBody.Bytes()

	for _, quoted := range []bool{false, true} {
		mode := "direct"
		if quoted {
			mode = "quoted"
		}
		for _, nested := range []bool{false, true} {
			level := "top_level"
			if nested {
				level = "nested"
			}
			for _, kind := range []string{"image", "post"} {
				t.Run(mode+"/"+level+"/"+kind, func(t *testing.T) {
					item := func(id, parent, msgType, content string) map[string]any {
						return map[string]any{
							"message_id": id, "upper_message_id": parent, "msg_type": msgType,
							"body": map[string]any{"content": content},
						}
					}
					items := []map[string]any{
						item("om_root", "", "merge_forward", ""),
						item("om_text", "om_root", "text", `{"text":"forwarded context"}`),
					}
					parent := "om_root"
					if nested {
						items = append(items, item("om_nested", parent, "merge_forward", ""))
						parent = "om_nested"
					}
					content := `{"image_key":"img_forward"}`
					wantFiles := map[string]string{"notes.txt": "forwarded file bytes"}
					if kind == "post" {
						content = `{"content":[[{"tag":"text","text":"image caption"},{"tag":"img","image_key":"img_forward"}]],"files":[{"file_key":"fk_post","file_name":"data.csv"}]}`
						wantFiles["data.csv"] = "column\nvalue\n"
					}
					items = append(items,
						item("om_image_child", parent, kind, content),
						item("om_file_child", parent, "file", `{"file_key":"fk_notes","file_name":"notes.txt"}`),
					)

					type resourceRequest struct{ path, kind string }
					var mu sync.Mutex
					var requests []resourceRequest
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						switch {
						case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
							w.Header().Set("Content-Type", "application/json")
							writeJSON(t, w, map[string]any{"code": 0, "expire": 7200, "tenant_access_token": "test-forward-image-token"})
						case r.URL.Path == "/open-apis/im/v1/messages/om_root":
							w.Header().Set("Content-Type", "application/json")
							writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"items": items}})
						case strings.Contains(r.URL.Path, "/resources/"):
							mu.Lock()
							requests = append(requests, resourceRequest{r.URL.Path, r.URL.Query().Get("type")})
							mu.Unlock()
							if !strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/messages/om_root/resources/") {
								w.Header().Set("Content-Type", "application/json")
								w.WriteHeader(http.StatusBadRequest)
								writeJSON(t, w, map[string]any{"code": 234003, "msg": "File not in msg."})
								return
							}
							var body []byte
							wantType := "file"
							switch r.URL.Path {
							case "/open-apis/im/v1/messages/om_root/resources/img_forward":
								body, wantType = imageData, "image"
							case "/open-apis/im/v1/messages/om_root/resources/fk_notes":
								body = []byte(wantFiles["notes.txt"])
							case "/open-apis/im/v1/messages/om_root/resources/fk_post":
								body = []byte(wantFiles["data.csv"])
							default:
								t.Errorf("unexpected resource path %s", r.URL.Path)
								w.WriteHeader(http.StatusNotFound)
								return
							}
							if got := r.URL.Query().Get("type"); got != wantType {
								t.Errorf("resource type = %q, want %q", got, wantType)
							}
							w.Header().Set("Content-Type", http.DetectContentType(body))
							if _, err := w.Write(body); err != nil {
								t.Errorf("write resource: %v", err)
							}
						default:
							t.Errorf("unexpected request %s", r.URL.Path)
							w.WriteHeader(http.StatusNotFound)
						}
					}))
					t.Cleanup(srv.Close)
					p := newTestPlatformForDownload(srv.URL, 8*1024*1024, 512*1024*1024)
					p.client = lark.NewClient("test-forward-image-client", "unused",
						lark.WithEnableTokenCache(false), lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client()))
					var text string
					var images []core.ImageAttachment
					if quoted {
						msg := p.fetchSingleMessage(context.Background(), "om_root")
						if msg == nil {
							t.Fatal("quoted forward context was dropped")
						}
						text, images = msg.text, msg.images
						if len(msg.files) != 0 {
							t.Errorf("quoted forward returned %d files, want none", len(msg.files))
						}
					} else {
						var files []core.FileAttachment
						text, images, files = p.parseMergeForward("om_root")
						if len(files) != len(wantFiles) {
							t.Errorf("files = %d, want %d", len(files), len(wantFiles))
						}
						seen := make(map[string]bool)
						for _, file := range files {
							want, ok := wantFiles[file.FileName]
							if !ok || string(file.Data) != want || seen[file.FileName] {
								t.Errorf("unexpected file %q bytes %q", file.FileName, file.Data)
							}
							seen[file.FileName] = true
						}
						for name := range wantFiles {
							if !seen[name] {
								t.Errorf("missing file %q", name)
							}
						}
					}
					if len(images) != 1 {
						t.Errorf("images = %d, want 1", len(images))
					} else if !bytes.Equal(images[0].Data, imageData) || images[0].MimeType != "image/png" {
						t.Errorf("image bytes or MIME changed: %+v", images[0])
					}
					if !strings.Contains(text, "forwarded context") || strings.Contains(text, "download failed") {
						t.Errorf("forwarded text lost context or contains download failure: %q", text)
					}
					if kind == "post" && !strings.Contains(text, "image caption") {
						t.Errorf("forwarded post lost its caption: %q", text)
					}
					if nested && !strings.Contains(text, "[forwarded messages]") {
						t.Errorf("nested forward marker missing: %q", text)
					}
					if quoted {
						for name := range wantFiles {
							if !strings.Contains(text, name) {
								t.Errorf("quoted forward lost file name %q: %q", name, text)
							}
						}
					}
					mu.Lock()
					defer mu.Unlock()
					var imageRequests, fileRequests int
					for _, req := range requests {
						if !strings.HasPrefix(req.path, "/open-apis/im/v1/messages/om_root/resources/") {
							t.Errorf("resource path = %q, want outer container om_root", req.path)
						}
						if req.kind == "image" {
							imageRequests++
						} else {
							fileRequests++
						}
					}
					wantFileRequests := len(wantFiles)
					if quoted {
						wantFileRequests = 0
					}
					if imageRequests != 1 || fileRequests != wantFileRequests {
						t.Errorf("resource requests = %d images, %d files; want 1 image, %d files", imageRequests, fileRequests, wantFileRequests)
					}
				})
			}
		}
	}
}
