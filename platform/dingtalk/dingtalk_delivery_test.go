package dingtalk

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/chatbot"
)

// TestReply_PreservesFreshAndLegacyWebhook keeps normal replies and explicit mentions compatible.
func TestReply_PreservesFreshAndLegacyWebhook(t *testing.T) {
	for _, expiry := range []int64{0, time.Now().Add(time.Hour).UnixMilli()} {
		t.Run(time.UnixMilli(expiry).String(), func(t *testing.T) {
			calls := 0
			p := deliveryPlatform(t, func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Path != "/session" {
					t.Fatalf("unexpected proactive request: %s", req.URL.Path)
				}
				var payload struct {
					At struct {
						UserIDs []string `json:"atUserIds"`
						All     bool     `json:"isAtAll"`
					} `json:"at"`
				}
				if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if len(payload.At.UserIDs) != 1 || payload.At.UserIDs[0] != "test-user" || payload.At.All {
					t.Fatalf("explicit mention changed: %+v", payload.At)
				}
				return deliveryResponse(http.StatusOK, `{"errcode":0,"errmsg":"ok"}`), nil
			})
			err := p.ReplyWithAt(context.Background(), replyContext{
				sessionWebhook: "https://example.invalid/session", sessionWebhookExpiredTime: expiry,
			}, "hello", []string{"test-user"}, false)
			if err != nil || calls != 1 {
				t.Fatalf("error = %v, requests = %d", err, calls)
			}
		})
	}
}

// TestReplyContext_ExpiryBoundary checks the provider timestamp with the request safety margin.
func TestReplyContext_ExpiryBoundary(t *testing.T) {
	for _, tc := range []struct {
		name string
		rc   replyContext
		want bool
	}{
		{"missing_webhook", replyContext{}, true},
		{"reconstructed", replyContext{sessionWebhook: "unused", proactive: true}, true},
		{"legacy", replyContext{sessionWebhook: "session"}, false},
		{"fresh", replyContext{sessionWebhook: "session", sessionWebhookExpiredTime: time.Now().Add(time.Hour).UnixMilli()}, false},
		{"expiring", replyContext{sessionWebhook: "session", sessionWebhookExpiredTime: time.Now().Add(10 * time.Second).UnixMilli()}, true},
		{"expired", replyContext{sessionWebhook: "session", sessionWebhookExpiredTime: time.Now().Add(-time.Second).UnixMilli()}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.rc.useProactiveAPI(); got != tc.want {
				t.Fatalf("useProactiveAPI = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestOnMessage_PreservesWebhookExpiry covers media and recognition fallback contexts too.
func TestOnMessage_PreservesWebhookExpiry(t *testing.T) {
	for _, kind := range []string{"richText", "picture", "audio", "audio_fallback", "file"} {
		t.Run(kind, func(t *testing.T) {
			p := deliveryPlatform(t, func(req *http.Request) (*http.Response, error) {
				switch req.URL.Path {
				case "/v1.0/robot/messageFiles/download":
					if kind == "audio_fallback" {
						return deliveryResponse(500, `{}`), nil
					}
					return deliveryResponse(200, `{"downloadUrl":"https://example.invalid/media"}`), nil
				case "/media":
					resp := deliveryResponse(200, "test media")
					resp.Header.Set("Content-Type", "image/png")
					return resp, nil
				default:
					t.Fatalf("unexpected request: %s", req.URL.Path)
					return nil, errors.New("unexpected request")
				}
			})
			var captured *core.Message
			p.handler = func(_ core.Platform, msg *core.Message) { captured = msg }
			msgtype := kind
			if kind == "audio_fallback" {
				msgtype = "audio"
			}
			p.onMessage(&chatbot.BotCallbackDataModel{
				MsgId: "incoming-media", Msgtype: msgtype, SenderStaffId: "test-user",
				ConversationId: "test-conversation", ConversationType: "2",
				SessionWebhook: "https://example.invalid/session", SessionWebhookExpiredTime: 1700000000123,
				Content: map[string]interface{}{
					"downloadCode": "test-download", "recognition": "recognized text", "fileName": "test.png",
					"richText": []interface{}{map[string]interface{}{"text": "hello"}},
				},
			}, nil, "test-robot")
			if captured == nil {
				t.Fatal("missing incoming message")
			}
			rc := captured.ReplyCtx.(replyContext)
			if rc.sessionWebhookExpiredTime != 1700000000123 {
				t.Fatalf("expiry = %d, want callback expiry", rc.sessionWebhookExpiredTime)
			}
		})
	}
}

// deliveryTransport intercepts both webhook and OpenAPI requests without a live robot.
type deliveryTransport func(*http.Request) (*http.Response, error)

// RoundTrip implements http.RoundTripper for deterministic delivery tests.
func (f deliveryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// deliveryResponse returns an isolated HTTP response for a provider result.
func deliveryResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

// deliveryPlatform replaces HTTP boundaries only; token and routing logic remain real.
func deliveryPlatform(t *testing.T, transport deliveryTransport) *Platform {
	t.Helper()
	client := &http.Client{Transport: transport}
	previous := core.HTTPClient
	core.HTTPClient = client
	t.Cleanup(func() { core.HTTPClient = previous })
	return &Platform{
		allowFrom: "*", robotCode: "test-robot", httpClient: client,
		accessToken: "test-access-token", tokenExpiry: time.Now().Add(time.Hour),
	}
}

// TestSend_ExpiredSessionWebhookUsesProactiveAPI reproduces a cached incoming context after idle.
func TestSend_ExpiredSessionWebhookUsesProactiveAPI(t *testing.T) {
	for _, method := range []string{"send", "reply", "reply_with_at"} {
		for _, conversation := range []string{"group", "direct"} {
			t.Run(method+"/"+conversation, func(t *testing.T) {
				var paths []string
				p := deliveryPlatform(t, func(req *http.Request) (*http.Response, error) {
					paths = append(paths, req.URL.Path)
					return deliveryResponse(http.StatusOK, `{"errcode":0,"processQueryKey":"receipt-1"}`), nil
				})
				var cached any
				p.handler = func(_ core.Platform, msg *core.Message) { cached = msg.ReplyCtx }
				conversationType, wantPath := "2", "/v1.0/robot/groupMessages/send"
				if conversation == "direct" {
					conversationType, wantPath = "1", "/v1.0/robot/oToMessages/batchSend"
				}
				p.onMessage(&chatbot.BotCallbackDataModel{
					MsgId: "incoming-1", Msgtype: "text", SenderStaffId: "test-user",
					ConversationId: "test-conversation", ConversationType: conversationType,
					SessionWebhook:            "https://example.invalid/session?token=webhook-secret",
					SessionWebhookExpiredTime: time.Now().Add(-time.Minute).UnixMilli(),
					Text:                      chatbot.BotCallbackDataTextModel{Content: "hello"},
				}, nil, "test-robot")
				if cached == nil {
					t.Fatal("incoming callback did not produce a reply context")
				}
				var err error
				switch method {
				case "send":
					err = p.Send(context.Background(), cached, "idle notification")
				case "reply":
					err = p.Reply(context.Background(), cached, "idle notification")
				case "reply_with_at":
					err = p.ReplyWithAt(context.Background(), cached, "idle notification", nil, false)
				}
				if err != nil {
					t.Fatal(err)
				}
				if len(paths) != 1 || paths[0] != wantPath {
					t.Fatalf("requests = %v, want one request to %s (not expired webhook)", paths, wantPath)
				}
			})
		}
	}
}

// TestReply_RejectsFailedWebhookResponses prevents HTTP 200 from hiding provider failures.
func TestReply_RejectsFailedWebhookResponses(t *testing.T) {
	for _, method := range []string{"reply", "reply_with_at"} {
		for _, body := range []string{`{"errcode":310000,"errmsg":"rejected"}`, `{}`, `null`, ``, `not-json`} {
			t.Run(method+"/"+body, func(t *testing.T) {
				calls := 0
				p := deliveryPlatform(t, func(_ *http.Request) (*http.Response, error) {
					calls++
					return deliveryResponse(http.StatusOK, body), nil
				})
				rc := replyContext{sessionWebhook: "https://example.invalid/session"}
				var err error
				if method == "reply" {
					err = p.Reply(context.Background(), rc, "message")
				} else {
					err = p.ReplyWithAt(context.Background(), rc, "message", []string{"test-user"}, false)
				}
				if err == nil {
					t.Fatal("expected provider response to be reported as a failure")
				}
				if calls != 1 {
					t.Fatalf("requests = %d, want no blind fallback or retry", calls)
				}
			})
		}
	}
}

// TestSend_ValidatesProactiveReceipt checks accepted, rejected and ambiguous OpenAPI results.
func TestSend_ValidatesProactiveReceipt(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		wantError bool
	}{
		{"accepted", 200, `{"processQueryKey":"receipt-1"}`, false},
		{"business_error", 200, `{"code":"Forbidden","message":"private provider details"}`, true},
		{"invalid_recipient", 200, `{"processQueryKey":"receipt-1","invalidStaffIdList":["private-user"]}`, true},
		{"rate_limited_recipient", 200, `{"processQueryKey":"receipt-1","flowControlledStaffIdList":["private-user"]}`, true},
		{"permission_denied", 403, `{"code":"Forbidden","message":"private provider details"}`, true},
		{"missing_receipt", 200, `{}`, true},
		{"invalid_json", 200, `not-json`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			p := deliveryPlatform(t, func(_ *http.Request) (*http.Response, error) {
				calls++
				return deliveryResponse(tc.status, tc.body), nil
			})
			err := p.Send(context.Background(), replyContext{proactive: true, senderStaffId: "test-user"}, "message")
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v, wantError %v", err, tc.wantError)
			}
			if err != nil && (strings.Contains(err.Error(), "private-user") || strings.Contains(err.Error(), "private provider details")) {
				t.Fatalf("error includes raw private provider response: %v", err)
			}
			if calls != 1 {
				t.Fatalf("requests = %d, want exactly one", calls)
			}
		})
	}
}

// TestReply_TransportFailureDoesNotRetry avoids duplicate sends after an ambiguous timeout.
func TestReply_TransportFailureDoesNotRetry(t *testing.T) {
	calls := 0
	p := deliveryPlatform(t, func(_ *http.Request) (*http.Response, error) {
		calls++
		return nil, context.DeadlineExceeded
	})
	err := p.Reply(context.Background(), replyContext{sessionWebhook: "https://example.invalid/session"}, "message")
	if !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Fatalf("error = %v, requests = %d; want timeout without retry", err, calls)
	}
}
