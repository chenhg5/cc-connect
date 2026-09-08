package qqbot

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestHandleMessage_QuotedImageAttachments(t *testing.T) {
	for _, surface := range []string{"group", "c2c"} {
		t.Run(surface, func(t *testing.T) {
			for _, tc := range []struct {
				name       string
				quotedType bool
				current    []string
				elements   [][]string
				reference  string
				want       []string
			}{
				{name: "quoted image with empty current text", quotedType: true, elements: [][]string{{"quoted.png"}}, want: []string{"/quoted.png"}},
				{name: "reference message", reference: "message", want: []string{"/reference.png"}},
				{name: "reference referenced_message", reference: "referenced_message", want: []string{"/reference.png"}},
				{name: "reference source_message", reference: "source_message", want: []string{"/reference.png"}},
				{name: "current and quoted image", quotedType: true, current: []string{"current.png"}, elements: [][]string{{"quoted.png"}}, want: []string{"/current.png", "/quoted.png"}},
				{name: "duplicate URL across all sources", quotedType: true, current: []string{"reference.png", "reference.png"}, reference: "message", elements: [][]string{{"reference.png"}}, want: []string{"/reference.png"}},
				{name: "nonquote elements ignored", current: []string{"current.png"}, elements: [][]string{{"ignored.png"}}, want: []string{"/current.png"}},
				{name: "only element zero belongs to quote", quotedType: true, elements: [][]string{{"quoted.png"}, {"ignored.png"}}, want: []string{"/quoted.png"}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					var requested []string
					var requestedMu sync.Mutex
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requestedMu.Lock()
						requested = append(requested, r.URL.Path)
						requestedMu.Unlock()
						w.Header().Set("Content-Type", "image/png")
						_, _ = w.Write([]byte(r.URL.Path))
					}))
					defer server.Close()
					originalClient := core.HTTPClient
					core.HTTPClient = server.Client()
					t.Cleanup(func() { core.HTTPClient = originalClient })
					attachments := func(names []string) []map[string]any {
						var result []map[string]any
						for _, name := range names {
							result = append(result, map[string]any{"content_type": "image/png", "url": server.URL + "/" + name})
						}
						return result
					}
					payload := map[string]any{
						"id": "new-message", "timestamp": time.Now().Format(time.RFC3339),
						"content": "", "attachments": attachments(tc.current),
						"author": map[string]any{"user_openid": "user-1", "member_openid": "user-1"},
					}
					if surface == "group" {
						payload["group_openid"] = "group-1"
						payload["content"] = "<@!bot123> "
					}
					if tc.quotedType {
						payload["message_type"] = msgTypeQuote
					}
					if tc.reference != "" {
						payload["message_reference"] = map[string]any{
							"message_id": "original-message",
							tc.reference: map[string]any{"attachments": attachments([]string{"reference.png"})},
						}
					}
					var elements []map[string]any
					for _, names := range tc.elements {
						elements = append(elements, map[string]any{"attachments": attachments(names)})
					}
					payload["msg_elements"] = elements
					var received *core.Message
					p := &Platform{allowFrom: "*"}
					p.handler = func(_ core.Platform, msg *core.Message) { received = msg }
					data, err := json.Marshal(payload)
					if err != nil {
						t.Fatal(err)
					}
					if surface == "group" {
						p.handleGroupMessage(data)
					} else {
						p.handleC2CMessage(data)
					}
					if received == nil {
						t.Fatal("quoted image was not delivered to the agent")
					}
					var imageBodies []string
					for _, image := range received.Images {
						imageBodies = append(imageBodies, string(image.Data))
					}
					requestedMu.Lock()
					defer requestedMu.Unlock()
					if !reflect.DeepEqual(imageBodies, tc.want) || !reflect.DeepEqual(requested, tc.want) {
						t.Fatalf("images = %v, requests = %v, want %v", imageBodies, requested, tc.want)
					}
				})
			}
		})
	}
}
