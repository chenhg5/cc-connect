package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
)

type galleryMessage struct {
	Path, Query string
	Body        map[string]any
}

type galleryServer struct {
	mu                                      sync.Mutex
	uploads                                 [][]byte
	messages                                []galleryMessage
	authCalls                               int
	failUploadAt, emptyKeyAt, failMessageAt int
	noMessageID                             bool
	retry                                   string
}

func (s *galleryServer) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/open-apis/auth/v3/tenant_access_token/internal":
		s.authCalls++
		writeJSON(t, w, map[string]any{"code": 0, "expire": 7200, "tenant_access_token": fmt.Sprintf("fixture-token-%d", s.authCalls)})
	case "/open-apis/im/v1/images":
		f, _, err := r.FormFile("image")
		if err != nil {
			t.Error(err)
			http.Error(w, "invalid upload", 400)
			return
		}
		defer func() {
			if err := f.Close(); err != nil {
				t.Error(err)
			}
			if err := r.MultipartForm.RemoveAll(); err != nil {
				t.Error(err)
			}
		}()
		data, err := io.ReadAll(f)
		if err != nil {
			t.Error(err)
			return
		}
		s.uploads = append(s.uploads, data)
		n := len(s.uploads)
		if n == s.failUploadAt {
			writeJSON(t, w, map[string]any{"code": 234001, "msg": "image rejected"})
			return
		}
		key := fmt.Sprintf("img_%d", n)
		if n == s.emptyKeyAt {
			key = ""
		}
		writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"image_key": key}})
	default:
		if r.URL.Path != "/open-apis/im/v1/messages" && !strings.HasSuffix(r.URL.Path, "/reply") {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", 400)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		s.messages = append(s.messages, galleryMessage{r.URL.Path, r.URL.RawQuery, body})
		n := len(s.messages)
		if s.retry == "eof" && n == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close() // The request was accepted, but its response was lost.
			return
		}
		if s.retry == "token" && n <= 2 {
			writeJSON(t, w, map[string]any{"code": 99991663, "msg": "Invalid access token for authorization"})
			return
		}
		if n == s.failMessageAt {
			writeJSON(t, w, map[string]any{"code": 230099, "msg": "card rejected"})
			return
		}
		data := map[string]any{}
		if !s.noMessageID {
			data["message_id"] = fmt.Sprintf("om_%d", n)
		}
		writeJSON(t, w, map[string]any{"code": 0, "data": data})
	}
}

func newGalleryPlatform(t *testing.T, name string, opts map[string]any, s *galleryServer) core.Platform {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.serve(t, w, r) }))
	t.Cleanup(srv.Close)
	opts["app_id"], opts["app_secret"] = "gallery-"+t.Name(), "fixture-secret"
	platform, err := newPlatform(name, srv.URL, opts)
	if err != nil {
		t.Fatal(err)
	}
	var p *Platform
	switch v := platform.(type) {
	case *Platform:
		p = v
	case *interactivePlatform:
		p = v.Platform
	default:
		t.Fatalf("unexpected platform %T", platform)
	}
	p.client = lark.NewClient(p.appID, p.appSecret, lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client()))
	p.replayClient = lark.NewClient(p.appID, p.appSecret, lark.WithEnableTokenCache(false), lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client()))
	return platform
}

func galleryImages(n int) []core.ImageAttachment {
	images := make([]core.ImageAttachment, n)
	for i := range images {
		images[i] = core.ImageAttachment{FileName: fmt.Sprintf("original-%d.png", i), MimeType: "image/png", Data: []byte{0x89, 'P', 'N', 'G', byte(i)}}
	}
	return images
}

type galleryAgent struct{}

func (*galleryAgent) Name() string { return "gallery-test" }
func (*galleryAgent) StartSession(context.Context, string) (core.AgentSession, error) {
	return nil, errors.New("unexpected agent startup")
}
func (*galleryAgent) ListSessions(context.Context) ([]core.AgentSessionInfo, error) { return nil, nil }
func (*galleryAgent) Stop() error                                                   { return nil }

func galleryEngine(p core.Platform) *core.Engine {
	return core.NewEngine("gallery-test", &galleryAgent{}, []core.Platform{p}, "", core.LangEnglish)
}

func galleryKeys(t *testing.T, message galleryMessage) []string {
	t.Helper()
	if message.Body["msg_type"] != "interactive" {
		t.Fatalf("not a gallery: %v", message.Body)
	}
	content, ok := message.Body["content"].(string)
	if !ok {
		t.Fatal("missing card content")
	}
	var card struct {
		Schema string `json:"schema"`
		Body   struct {
			Elements []struct {
				Tag    string `json:"tag"`
				Mode   string `json:"combination_mode"`
				Images []struct {
					Key string `json:"img_key"`
				} `json:"img_list"`
			} `json:"elements"`
		} `json:"body"`
	}
	if err := json.Unmarshal([]byte(content), &card); err != nil {
		t.Fatal(err)
	}
	if card.Schema != "2.0" || len(card.Body.Elements) != 1 {
		t.Fatalf("invalid card: %s", content)
	}
	el := card.Body.Elements[0]
	if el.Tag != "img_combination" || el.Mode != "bisect" {
		t.Fatalf("invalid gallery component: %s", content)
	}
	var keys []string
	for _, img := range el.Images {
		keys = append(keys, img.Key)
	}
	return keys
}

func TestImageGallery_ExplicitOptInAndLegacyCompatibility(t *testing.T) {
	for _, name := range []string{"feishu", "lark"} {
		for _, mode := range []string{"omitted", "false", "true", "cards-disabled"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				s := &galleryServer{}
				opts := map[string]any{}
				if mode != "omitted" {
					opts["enable_image_gallery"] = mode != "false"
				}
				if mode == "cards-disabled" {
					opts["enable_feishu_card"] = false
				}
				p := newGalleryPlatform(t, name, opts, s)
				e := galleryEngine(p)
				if err := e.SendToSessionWithAttachments(name+":oc_gallery", "", galleryImages(10), nil, nil, false); err != nil {
					t.Fatal(err)
				}
				s.mu.Lock()
				defer s.mu.Unlock()
				want := 10
				if mode == "true" {
					want = 2
				}
				if len(s.messages) != want || len(s.uploads) != 10 {
					t.Fatalf("messages=%d uploads=%d", len(s.messages), len(s.uploads))
				}
				for i, m := range s.messages {
					if mode == "true" {
						if keys := galleryKeys(t, m); len(keys) != 5 {
							t.Fatalf("group %d has %d images", i, len(keys))
						}
					} else if m.Body["msg_type"] != "image" || m.Body["uuid"] != nil {
						t.Fatalf("legacy wire format changed: %v", m.Body)
					}
				}
			})
		}
	}
	if _, err := New(map[string]any{"app_id": "fixture", "app_secret": "fixture", "enable_image_gallery": "true"}); err == nil {
		t.Fatal("non-boolean opt-in accepted")
	}
}

func TestImageGallery_OriginalsOrderAndTopic(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		rc                     replyContext
		noReply, thread, reply bool
	}{
		{"chat", replyContext{chatID: "oc_gallery"}, false, false, false},
		{"quote", replyContext{chatID: "oc_gallery", messageID: "om_request"}, false, false, true},
		{"quote-disabled", replyContext{chatID: "oc_gallery", messageID: "om_request"}, true, false, false},
		{"topic", replyContext{chatID: "oc_gallery", messageID: "om_request", sessionKey: "feishu:oc_gallery:root:om_root"}, false, true, true},
		{"topic-without-quote", replyContext{chatID: "oc_gallery", messageID: "om_request", sessionKey: "feishu:oc_gallery:root:om_root"}, true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &galleryServer{}
			p := newGalleryPlatform(t, "feishu", map[string]any{"enable_image_gallery": true, "thread_isolation": tc.thread, "reply_to_trigger": !tc.noReply}, s)
			images := galleryImages(5)
			for _, group := range [][]core.ImageAttachment{images, images[:1]} {
				if err := p.(core.ImageBatchSender).SendImageBatch(context.Background(), tc.rc, group); err != nil {
					t.Fatal(err)
				}
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if len(s.messages) != 2 || len(s.uploads) != 6 {
				t.Fatalf("messages=%d uploads=%d", len(s.messages), len(s.uploads))
			}
			for i, data := range s.uploads {
				if !reflect.DeepEqual(data, images[i%5].Data) {
					t.Fatal("original bytes or upload order changed")
				}
			}
			if keys := galleryKeys(t, s.messages[0]); !reflect.DeepEqual(keys, []string{"img_1", "img_2", "img_3", "img_4", "img_5"}) {
				t.Fatalf("wrong gallery order: %v", keys)
			}
			if keys := galleryKeys(t, s.messages[1]); !reflect.DeepEqual(keys, []string{"img_6"}) {
				t.Fatalf("wrong final single-image group: %v", keys)
			}
			for _, m := range s.messages {
				if tc.reply {
					if m.Path != "/open-apis/im/v1/messages/om_request/reply" || m.Body["reply_in_thread"] != nil && m.Body["reply_in_thread"] != tc.thread {
						t.Fatalf("wrong reply target: %+v", m)
					}
					if tc.thread && m.Body["reply_in_thread"] != true {
						t.Fatal("gallery escaped topic")
					}
				} else if m.Path != "/open-apis/im/v1/messages" || m.Body["receive_id"] != "oc_gallery" || m.Query != "receive_id_type=chat_id" {
					t.Fatalf("wrong chat target: %+v", m)
				}
				if id, _ := m.Body["uuid"].(string); id == "" {
					t.Fatal("missing delivery ID")
				}
			}
			if s.messages[0].Body["uuid"] == s.messages[1].Body["uuid"] {
				t.Fatal("independent groups reused delivery ID")
			}
		})
	}
}

func TestImageGallery_InternalRetriesKeepIDAndContent(t *testing.T) {
	for _, reply := range []bool{false, true} {
		for _, retry := range []string{"eof", "token"} {
			t.Run(fmt.Sprintf("reply=%t/%s", reply, retry), func(t *testing.T) {
				s := &galleryServer{retry: retry}
				p := newGalleryPlatform(t, "feishu", map[string]any{"enable_image_gallery": true}, s)
				rc := replyContext{chatID: "oc_gallery"}
				if reply {
					rc.messageID = "om_request"
				}
				if err := p.(core.ImageBatchSender).SendImageBatch(context.Background(), rc, galleryImages(3)); err != nil {
					t.Fatal(err)
				}
				s.mu.Lock()
				defer s.mu.Unlock()
				if len(s.messages) < 2 || len(s.uploads) != 3 {
					t.Fatalf("attempts=%d uploads=%d", len(s.messages), len(s.uploads))
				}
				if retry == "token" && s.authCalls < 2 {
					t.Fatal("did not refresh token")
				}
				if id, _ := s.messages[0].Body["uuid"].(string); id == "" {
					t.Fatal("missing retry ID")
				}
				for _, m := range s.messages[1:] {
					if !reflect.DeepEqual(m, s.messages[0]) {
						t.Fatal("retry changed delivery ID, content or target")
					}
				}
			})
		}
	}
}

func TestImageGallery_FailureReportsOnlyConfirmedImages(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		server                  *galleryServer
		sent, uploads, messages int
	}{
		{"upload", &galleryServer{failUploadAt: 7}, 5, 7, 1},
		{"empty-key", &galleryServer{emptyKeyAt: 2}, 0, 2, 0},
		{"message", &galleryServer{failMessageAt: 2}, 5, 10, 2},
		{"unconfirmed", &galleryServer{noMessageID: true}, 0, 5, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.server
			p := newGalleryPlatform(t, "feishu", map[string]any{"enable_image_gallery": true, "thread_isolation": true, "reply_to_trigger": false}, s)
			e := galleryEngine(p)
			err := e.SendToSessionWithAttachments("feishu:oc_gallery:root:om_root", "", galleryImages(11), []core.FileAttachment{{FileName: "after.txt", Data: []byte("file")}}, nil, false)
			var partial *core.ImageDeliveryError
			if !errors.As(err, &partial) || partial.Sent != tc.sent || partial.Total != 11 {
				t.Fatalf("wrong partial result: %v", err)
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if len(s.uploads) != tc.uploads || len(s.messages) != tc.messages {
				t.Fatalf("delivery continued or replayed: uploads=%d messages=%d", len(s.uploads), len(s.messages))
			}
			for _, m := range s.messages {
				if m.Path != "/open-apis/im/v1/messages/om_root/reply" || m.Body["reply_in_thread"] != true {
					t.Fatalf("reconstructed topic lost: %+v", m)
				}
			}
		})
	}
}

func TestImageGallery_RejectsInvalidBatchBeforeUpload(t *testing.T) {
	for _, name := range []string{"empty", "oversized", "bad-context", "no-target", "canceled"} {
		t.Run(name, func(t *testing.T) {
			s := &galleryServer{}
			p := newGalleryPlatform(t, "feishu", map[string]any{"enable_image_gallery": true}, s)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var rc any = replyContext{chatID: "oc_gallery"}
			images := galleryImages(2)
			switch name {
			case "empty":
				images = nil
			case "oversized":
				images = galleryImages(6)
			case "bad-context":
				rc = "wrong"
			case "no-target":
				rc = replyContext{}
			case "canceled":
				cancel()
			}
			if err := p.(core.ImageBatchSender).SendImageBatch(ctx, rc, images); err == nil {
				t.Fatal("invalid batch accepted")
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.authCalls != 0 || len(s.uploads) != 0 || len(s.messages) != 0 {
				t.Fatal("invalid batch reached transport")
			}
		})
	}
}

func TestImageGallery_SingleImageStillUsesNativeMessage(t *testing.T) {
	s := &galleryServer{}
	p := newGalleryPlatform(t, "feishu", map[string]any{"enable_image_gallery": true}, s)
	if err := galleryEngine(p).SendToSessionWithAttachments("feishu:oc_gallery", "", galleryImages(1), nil, nil, false); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.messages) != 1 || s.messages[0].Body["msg_type"] != "image" || s.messages[0].Body["uuid"] != nil {
		t.Fatalf("single image changed: %+v", s.messages)
	}
}
