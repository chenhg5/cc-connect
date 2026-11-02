package feishu

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
)

// TestDispatchMessageMediaDownloadsVideo verifies that a Feishu "media"
// (video) message forwards the video file itself to the agent, not just the
// cover thumbnail, and that a failed video download still dispatches the
// message with the thumbnail and a note.
func TestDispatchMessageMediaDownloadsVideo(t *testing.T) {
	const appID = "cli_media_video"
	const appSecret = "secret-media-video"
	const currentUser = "ou_alice"
	const fileKey = "file_v3_video"
	const imageKey = "img_v3_cover"

	// Minimal MP4-looking payload ("ftyp" box) and a PNG cover.
	videoData := []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isomfake video body")
	coverData := []byte("\x89PNG\r\n\x1a\nfake cover")

	var mu sync.Mutex
	videoCalls := map[string]int{} // messageID -> calls to the video resource endpoint
	failVideo := map[string]bool{} // messageID -> make the video download fail

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
		case strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/messages/") && strings.HasSuffix(r.URL.Path, "/resources/"+imageKey):
			if got := r.URL.Query().Get("type"); got != "image" {
				t.Errorf("cover resource type = %q, want image", got)
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(coverData)
		case strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/messages/") && strings.HasSuffix(r.URL.Path, "/resources/"+fileKey):
			if got := r.URL.Query().Get("type"); got != "file" {
				t.Errorf("video resource type = %q, want file", got)
			}
			mid := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/open-apis/im/v1/messages/"), "/resources/"+fileKey)
			mu.Lock()
			videoCalls[mid]++
			fail := failVideo[mid]
			mu.Unlock()
			if fail {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				writeJSON(t, w, map[string]any{"code": 234001, "msg": "invalid param"})
				return
			}
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(videoData)
		default:
			w.Header().Set("Content-Type", "application/json")
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success"})
		}
	}))
	defer srv.Close()

	newClient := func(handler func(_ core.Platform, msg *core.Message)) *Platform {
		return &Platform{
			platformName: "feishu",
			domain:       srv.URL,
			appID:        appID,
			appSecret:    appSecret,
			handler:      handler,
			client: lark.NewClient(appID, appSecret,
				lark.WithOpenBaseUrl(srv.URL),
				lark.WithHttpClient(srv.Client()),
			),
		}
	}

	dispatch := func(t *testing.T, messageID, content string) *core.Message {
		t.Helper()
		got := make(chan *core.Message, 1)
		p := newClient(func(_ core.Platform, msg *core.Message) { got <- msg })
		p.dispatchMessage(
			context.Background(),
			"media",
			content,
			nil,
			messageID,
			"feishu:oc_chat:"+currentUser,
			currentUser,
			"oc_chat",
			replyContext{messageID: messageID, sessionKey: "feishu:oc_chat:" + currentUser},
			"",
			0,
		)
		select {
		case msg := <-got:
			return msg
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for dispatched message")
			return nil
		}
	}

	content := `{"file_key":"` + fileKey + `","image_key":"` + imageKey + `","file_name":"clip.mp4","duration":9000}`

	t.Run("video file is attached alongside the cover", func(t *testing.T) {
		const mid = "om_media_ok"
		msg := dispatch(t, mid, content)

		if len(msg.Files) != 1 {
			t.Fatalf("len(Files) = %d, want 1 (the video itself)", len(msg.Files))
		}
		f := msg.Files[0]
		if f.FileName != "clip.mp4" {
			t.Errorf("Files[0].FileName = %q, want clip.mp4", f.FileName)
		}
		if f.MimeType != "video/mp4" {
			t.Errorf("Files[0].MimeType = %q, want video/mp4", f.MimeType)
		}
		if string(f.Data) != string(videoData) {
			t.Errorf("Files[0].Data does not match the mocked video body")
		}
		if len(msg.Images) != 1 || string(msg.Images[0].Data) != string(coverData) {
			t.Errorf("cover thumbnail not kept as image: Images=%d", len(msg.Images))
		}
		if !strings.Contains(msg.Content, "[video: clip.mp4, 9s]") {
			t.Errorf("Content = %q, want it to keep the [video: clip.mp4, 9s] marker", msg.Content)
		}
		mu.Lock()
		calls := videoCalls[mid]
		mu.Unlock()
		if calls == 0 {
			t.Errorf("video resource endpoint was never called")
		}
	})

	t.Run("failed video download still dispatches cover with a note", func(t *testing.T) {
		const mid = "om_media_fail"
		mu.Lock()
		failVideo[mid] = true
		mu.Unlock()
		msg := dispatch(t, mid, content)

		if len(msg.Files) != 0 {
			t.Errorf("len(Files) = %d, want 0 when the video download fails", len(msg.Files))
		}
		if len(msg.Images) != 1 {
			t.Errorf("len(Images) = %d, want the cover thumbnail to still be attached", len(msg.Images))
		}
		if !strings.Contains(msg.Content, "video download failed") {
			t.Errorf("Content = %q, want a note that the video download failed", msg.Content)
		}
	})
}
