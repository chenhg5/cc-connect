package ruliu

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// pngMagic is enough for http.DetectContentType to report image/png.
var pngMagic = append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, []byte("rest-of-image")...)

// attachmentServer serves the payloads referenced by the callback fixtures.
func attachmentServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/image":
			_, _ = w.Write(pngMagic)
		case "/file":
			_, _ = w.Write([]byte("file-body"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDownloadURL(t *testing.T) {
	srv := attachmentServer(t)
	p := &Platform{}
	ctx := context.Background()

	got, err := p.downloadURL(ctx, srv.URL+"/image")
	if err != nil {
		t.Fatalf("downloadURL: %v", err)
	}
	if !bytes.Equal(got, pngMagic) {
		t.Fatalf("downloadURL body = %q, want %q", got, pngMagic)
	}

	if _, err := p.downloadURL(ctx, srv.URL+"/missing"); err == nil {
		t.Fatal("expected error for non-200 response")
	}
	if _, err := p.downloadURL(ctx, "http://\x7f/bad"); err == nil {
		t.Fatal("expected error for unparsable URL")
	}
}

func TestDownloadAttachments(t *testing.T) {
	srv := attachmentServer(t)
	p := &Platform{}
	ctx := context.Background()

	items := []callbackBodyItem{
		{Type: "IMAGE", DownloadURL: srv.URL + "/image"},
		{Type: "IMAGE"}, // no download URL -> skipped
		{Type: "IMAGE", DownloadURL: srv.URL + "/missing"},             // download failure -> skipped
		{Type: "TEXT", Content: "hello", DownloadURL: srv.URL + "/ok"}, // not an attachment type
		{Type: "FILE", DownloadURL: srv.URL + "/file", Name: "report.bin"},
		{Type: "FILE", DownloadURL: srv.URL + "/file"}, // no name -> empty FileName
	}

	images := p.downloadImages(ctx, items)
	if len(images) != 1 {
		t.Fatalf("downloadImages returned %d images, want 1", len(images))
	}
	if images[0].MimeType != "image/png" {
		t.Fatalf("image MimeType = %q, want image/png", images[0].MimeType)
	}
	if !bytes.Equal(images[0].Data, pngMagic) {
		t.Fatalf("image data = %q, want %q", images[0].Data, pngMagic)
	}

	files := p.downloadFiles(ctx, items)
	if len(files) != 2 {
		t.Fatalf("downloadFiles returned %d files, want 2", len(files))
	}
	if files[0].FileName != "report.bin" {
		t.Fatalf("file name = %q, want report.bin", files[0].FileName)
	}
	if string(files[0].Data) != "file-body" {
		t.Fatalf("file data = %q, want file-body", files[0].Data)
	}
	if files[0].MimeType != "text/plain; charset=utf-8" {
		t.Fatalf("file MimeType = %q, want text/plain; charset=utf-8", files[0].MimeType)
	}
	if files[1].FileName != "" {
		t.Fatalf("file name = %q, want empty", files[1].FileName)
	}
}

// TestCallbackHandlerKeepsAttachmentOnlyMessages pins the behaviour this change
// adds: a callback with no text but with media must still reach the agent.
func TestCallbackHandlerKeepsAttachmentOnlyMessages(t *testing.T) {
	srv := attachmentServer(t)
	key := []byte(testAESKeyMaterial)
	p := &Platform{token: testCallbackToken, aesKey: key, skipVerify: true}

	var delivered []*core.Message
	p.handler = func(_ core.Platform, msg *core.Message) {
		delivered = append(delivered, msg)
	}

	cases := []struct {
		name        string
		body        []callbackBodyItem
		wantMessage bool
		wantImages  int
		wantFiles   int
		wantContent string
	}{
		{
			name:        "image only",
			body:        []callbackBodyItem{{Type: "IMAGE", DownloadURL: srv.URL + "/image"}},
			wantMessage: true,
			wantImages:  1,
		},
		{
			name:        "file only",
			body:        []callbackBodyItem{{Type: "FILE", DownloadURL: srv.URL + "/file", Name: "a.txt"}},
			wantMessage: true,
			wantFiles:   1,
		},
		{
			name:        "text still works",
			body:        []callbackBodyItem{{Type: "TEXT", Content: "@bot hello"}},
			wantMessage: true,
			wantContent: "@bot hello",
		},
		{
			name:        "text plus image",
			body:        []callbackBodyItem{{Type: "TEXT", Content: "look"}, {Type: "IMAGE", DownloadURL: srv.URL + "/image"}},
			wantMessage: true,
			wantImages:  1,
			wantContent: "look",
		},
		{
			name:        "nothing to forward",
			body:        []callbackBodyItem{{Type: "AT", UserID: "u2"}},
			wantMessage: false,
		},
		{
			name:        "media that failed to download is dropped",
			body:        []callbackBodyItem{{Type: "IMAGE", DownloadURL: srv.URL + "/missing"}},
			wantMessage: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			delivered = nil

			envelope := callbackEnvelope{
				EventType: "MESSAGE_RECEIVE",
				GroupID:   42,
				Time:      time.Now().Unix(),
				Message: callbackMessage{
					Header: callbackHeader{FromUserID: "u1", MessageID: time.Now().UnixNano()},
					Body:   c.body,
				},
			}
			payload, err := json.Marshal(envelope)
			if err != nil {
				t.Fatalf("marshal envelope: %v", err)
			}
			ciphertext := encryptECBWithPKCS7(t, payload, key)
			raw := base64.URLEncoding.EncodeToString(ciphertext)

			req := httptest.NewRequest(http.MethodPost, defaultCallbackURL, strings.NewReader(raw))
			rec := httptest.NewRecorder()
			p.callbackHandler(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if !c.wantMessage {
				if len(delivered) != 0 {
					t.Fatalf("handler called %d times, want 0", len(delivered))
				}
				return
			}
			if len(delivered) != 1 {
				t.Fatalf("handler called %d times, want 1", len(delivered))
			}
			msg := delivered[0]
			if len(msg.Images) != c.wantImages {
				t.Fatalf("images = %d, want %d", len(msg.Images), c.wantImages)
			}
			if len(msg.Files) != c.wantFiles {
				t.Fatalf("files = %d, want %d", len(msg.Files), c.wantFiles)
			}
			if msg.Content != c.wantContent {
				t.Fatalf("content = %q, want %q", msg.Content, c.wantContent)
			}
			if msg.SessionKey != "ruliu:42:u1" {
				t.Fatalf("SessionKey = %q, want ruliu:42:u1", msg.SessionKey)
			}
		})
	}
}

func TestStartTypingIsNonBlockingAndSafe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	p := &Platform{webhook: srv.URL}
	if stop := p.StartTyping(context.Background(), nil); stop == nil {
		t.Fatal("StartTyping returned nil stop func")
	} else {
		stop()
	}

	stop := p.StartTyping(context.Background(), replyContext{groupID: 7})
	if stop == nil {
		t.Fatal("StartTyping returned nil stop func for a valid reply context")
	}
	stop()
}
