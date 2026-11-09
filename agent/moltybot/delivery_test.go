package moltybot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

type deliveredReply struct {
	kind, name, content string
}

type deliveryPlatform struct {
	replies chan deliveredReply
}

func (p *deliveryPlatform) Name() string                    { return "test" }
func (p *deliveryPlatform) Start(core.MessageHandler) error { return nil }
func (p *deliveryPlatform) Stop() error                     { return nil }
func (p *deliveryPlatform) Reply(ctx context.Context, target any, text string) error {
	return p.Send(ctx, target, text)
}
func (p *deliveryPlatform) Send(_ context.Context, _ any, text string) error {
	p.replies <- deliveredReply{kind: "text", content: text}
	return nil
}
func (p *deliveryPlatform) SendImage(_ context.Context, _ any, image core.ImageAttachment) error {
	p.replies <- deliveredReply{kind: "image", name: image.FileName, content: string(image.Data)}
	return nil
}
func (p *deliveryPlatform) SendFile(_ context.Context, _ any, file core.FileAttachment) error {
	p.replies <- deliveredReply{kind: "file", name: file.FileName, content: string(file.Data)}
	return nil
}

// Exercise the real HTTP bridge, agent events, engine and platform capability
// dispatch across text-only, attachment-only and mixed replies in one session.
func TestBridgeResponseDeliveryPreservesAttachmentsAcrossTurns(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req bridgeMessageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		result := bridgeMessageResponse{OK: true, SessionKey: "remote:test:user"}
		if req.Text != "attachments only" {
			result.ReplyText = "reply: " + req.Text
		}
		if req.Text != "text only" {
			result.Attachments = []bridgeAttachment{
				{Kind: "image", Name: "picture.png", MimeType: "image/png", DataBase64: "aW1n"},
				{Kind: "file", Name: "report.pdf", MimeType: "application/pdf", DataBase64: "cGRm"},
			}
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()
	agent, err := New(map[string]any{"base_url": server.URL, "token": "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	p := &deliveryPlatform{replies: make(chan deliveredReply, 128)}
	e := core.NewEngine("bridge-delivery", agent, []core.Platform{p}, "", core.LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	for _, prompt := range []string{"text only", "attachments only", "text and attachments"} {
		e.ReceiveMessage(p, &core.Message{
			SessionKey: "test:user", Platform: p.Name(), UserID: "user",
			MessageID: prompt, Content: prompt, ReplyCtx: "user",
		})
		wantText := prompt != "attachments only"
		wantImage, wantFile := prompt != "text only", prompt != "text only"
		timer := time.NewTimer(5 * time.Second)
		for wantText || wantImage || wantFile {
			select {
			case reply := <-p.replies:
				switch reply.kind {
				case "text":
					if strings.Contains(reply.content, "(empty response)") {
						t.Fatalf("%s: spurious empty response: %q", prompt, reply.content)
					}
					if strings.Contains(reply.content, "reply: "+prompt) {
						wantText = false
					}
				case "image":
					if !wantImage || reply.name != "picture.png" || reply.content != "img" {
						t.Fatalf("%s: unexpected image: %#v", prompt, reply)
					}
					wantImage = false
				case "file":
					if !wantFile || reply.name != "report.pdf" || reply.content != "pdf" {
						t.Fatalf("%s: unexpected file: %#v", prompt, reply)
					}
					wantFile = false
				}
			case <-timer.C:
				t.Fatalf("%s: delivery timed out (text=%v image=%v file=%v)", prompt, wantText, wantImage, wantFile)
			}
		}
		timer.Stop()
	}
}
