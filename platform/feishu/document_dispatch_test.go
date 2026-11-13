package feishu

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
)

// Exercise the SDK's two custom-handler contracts: plaintext WebSocket payloads
// and the original encrypted webhook body after SDK signature verification.
func TestDocumentCommentSDKDispatch(t *testing.T) {
	for _, transport := range []string{"websocket", "webhook"} {
		t.Run(transport, func(t *testing.T) {
			called := make(chan struct{}, 2)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
					_, _ = io.WriteString(w, `{"code":0,"tenant_access_token":"dispatch-test","expire":7200}`)
					return
				}
				called <- struct{}{}
				_, _ = io.WriteString(w, `{"code":0,"data":{"items":[{"comment_id":"comment1","is_solved":true}]}}`)
			}))
			defer srv.Close()
			p := &Platform{enableDocumentComments: true, botOpenID: "ou_bot", client: lark.NewClient("cli_dispatch_"+transport, "secret", lark.WithOpenBaseUrl(srv.URL))}
			event, _ := json.Marshal(documentEvent(t))
			var envelope map[string]any
			if err := json.Unmarshal(event, &envelope); err != nil {
				t.Fatal(err)
			}
			envelope["schema"] = "2.0"
			envelope["header"] = map[string]string{"event_type": "drive.notice.comment_add_v1"}
			body, _ := json.Marshal(envelope)
			key := ""
			if transport == "webhook" {
				key = "document-callback-test"
			}
			p.encryptKey = key
			d := dispatcher.NewEventDispatcher("", key).OnCustomizedEvent("drive.notice.comment_add_v1", p.onDocumentCommentRequest)
			if transport == "websocket" {
				if _, err := d.Do(context.Background(), body); err != nil {
					t.Fatal(err)
				}
			} else {
				body = encryptCommentEvent(t, body, key)
				req := &larkevent.EventReq{Body: body, Header: map[string][]string{
					larkevent.EventRequestTimestamp: {"123"},
					larkevent.EventRequestNonce:     {"nonce"},
					larkevent.EventSignature:        {larkevent.Signature("123", "nonce", key, string(body))},
				}}
				response := d.Handle(context.Background(), req)
				if response.StatusCode != http.StatusOK {
					t.Fatalf("webhook failed: %s", response.Body)
				}
			}
			select {
			case <-called:
			case <-time.After(time.Second):
				t.Fatal("custom handler did not dispatch the mention")
			}
		})
	}
}

func encryptCommentEvent(t *testing.T, body []byte, key string) []byte {
	t.Helper()
	hash := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(hash[:])
	if err != nil {
		t.Fatal(err)
	}
	padding := aes.BlockSize - len(body)%aes.BlockSize
	body = append(body, bytes.Repeat([]byte{byte(padding)}, padding)...)
	iv := bytes.Repeat([]byte{1}, aes.BlockSize)
	output := make([]byte, len(body))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(output, body)
	encoded, _ := json.Marshal(map[string]string{"encrypt": base64.StdEncoding.EncodeToString(append(iv, output...))})
	return encoded
}

func TestDocumentCommentOptions(t *testing.T) {
	for _, limit := range []any{0, -1, 100001, "12000"} {
		if _, err := New(map[string]any{"app_id": "cli_options", "app_secret": "secret", "document_context_max_chars": limit}); err == nil {
			t.Fatalf("accepted invalid context budget: %v", limit)
		}
	}
}
