package feishu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func TestReplyRefreshesTenantTokenAfterInvalidCachedToken(t *testing.T) {
	const appID = "cli_reply_retry"
	const appSecret = "secret-reply-retry"

	authCalls := 0
	replyCalls := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			authCalls++
			token := "stale-token"
			if authCalls >= 2 {
				token = "fresh-token"
			}
			writeJSON(t, w, map[string]any{
				"code":                0,
				"msg":                 "success",
				"expire":              7200,
				"tenant_access_token": token,
			})
		case strings.HasSuffix(r.URL.Path, "/reply"):
			replyCalls++
			authz := r.Header.Get("Authorization")
			switch replyCalls {
			case 1, 2:
				if authz != "Bearer stale-token" {
					t.Fatalf("reply call %d Authorization = %q, want stale token", replyCalls, authz)
				}
				writeJSON(t, w, map[string]any{
					"code": 99991663,
					"msg":  "Invalid access token for authorization",
				})
			case 3:
				if authz != "Bearer fresh-token" {
					t.Fatalf("reply retry Authorization = %q, want fresh token", authz)
				}
				writeJSON(t, w, map[string]any{
					"code": 0,
					"msg":  "success",
					"data": map[string]any{"message_id": "om_reply_ok"},
				})
			default:
				t.Fatalf("unexpected extra reply call %d", replyCalls)
			}
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	p := &Platform{
		platformName: "feishu",
		domain:       srv.URL,
		appID:        appID,
		appSecret:    appSecret,
		client: lark.NewClient(appID, appSecret,
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
		replayClient: lark.NewClient(appID, appSecret,
			lark.WithEnableTokenCache(false),
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
	}

	err := p.Reply(context.Background(), replyContext{messageID: "om_root", chatID: "oc_chat"}, "hello")
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if authCalls != 2 {
		t.Fatalf("authCalls = %d, want 2", authCalls)
	}
	if replyCalls != 3 {
		t.Fatalf("replyCalls = %d, want 3", replyCalls)
	}
}

func TestSendNewMessageToChatRefreshesTenantTokenAfterInvalidCachedToken(t *testing.T) {
	const appID = "cli_create_retry"
	const appSecret = "secret-create-retry"

	authCalls := 0
	createCalls := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			authCalls++
			token := "stale-token"
			if authCalls >= 2 {
				token = "fresh-token"
			}
			writeJSON(t, w, map[string]any{
				"code":                0,
				"msg":                 "success",
				"expire":              7200,
				"tenant_access_token": token,
			})
		case r.URL.Path == "/open-apis/im/v1/messages":
			createCalls++
			authz := r.Header.Get("Authorization")
			switch createCalls {
			case 1, 2:
				if authz != "Bearer stale-token" {
					t.Fatalf("create call %d Authorization = %q, want stale token", createCalls, authz)
				}
				writeJSON(t, w, map[string]any{
					"code": 99991663,
					"msg":  "Invalid access token for authorization",
				})
			case 3:
				if authz != "Bearer fresh-token" {
					t.Fatalf("create retry Authorization = %q, want fresh token", authz)
				}
				writeJSON(t, w, map[string]any{
					"code": 0,
					"msg":  "success",
					"data": map[string]any{"message_id": "om_create_ok"},
				})
			default:
				t.Fatalf("unexpected extra create call %d", createCalls)
			}
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	p := &Platform{
		platformName: "feishu",
		domain:       srv.URL,
		appID:        appID,
		appSecret:    appSecret,
		client: lark.NewClient(appID, appSecret,
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
		replayClient: lark.NewClient(appID, appSecret,
			lark.WithEnableTokenCache(false),
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
	}

	err := p.sendNewMessageToChat(context.Background(), replyContext{chatID: "oc_chat"}, "text", `{"text":"hello"}`)
	if err != nil {
		t.Fatalf("sendNewMessageToChat() error = %v", err)
	}
	if authCalls != 2 {
		t.Fatalf("authCalls = %d, want 2", authCalls)
	}
	if createCalls != 3 {
		t.Fatalf("createCalls = %d, want 3", createCalls)
	}
}

func TestReplyDoesNotRefreshTenantTokenOnNonTokenError(t *testing.T) {
	const appID = "cli_non_token_error"
	const appSecret = "secret-non-token-error"

	authCalls := 0
	replyCalls := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			authCalls++
			writeJSON(t, w, map[string]any{
				"code":                0,
				"msg":                 "success",
				"expire":              7200,
				"tenant_access_token": "normal-token",
			})
		case strings.HasSuffix(r.URL.Path, "/reply"):
			replyCalls++
			writeJSON(t, w, map[string]any{
				"code": 230001,
				"msg":  "rate limited",
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	p := &Platform{
		platformName: "feishu",
		domain:       srv.URL,
		appID:        appID,
		appSecret:    appSecret,
		client: lark.NewClient(appID, appSecret,
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
		replayClient: lark.NewClient(appID, appSecret,
			lark.WithEnableTokenCache(false),
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
	}

	err := p.Reply(context.Background(), replyContext{messageID: "om_root", chatID: "oc_chat"}, "hello")
	if err == nil {
		t.Fatal("Reply() error = nil, want failure")
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("Reply() error = %v, want rate limited", err)
	}
	if authCalls > 1 {
		t.Fatalf("authCalls = %d, want <= 1", authCalls)
	}
	if replyCalls != 1 {
		t.Fatalf("replyCalls = %d, want 1", replyCalls)
	}
}

func TestIsTenantAccessTokenInvalid(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "code only", err: testError("feishu: reply failed code=99991663 msg=something"), want: true},
		{name: "message only", err: testError("feishu: reply api call: Invalid access token for authorization"), want: true},
		{name: "other error", err: testError("feishu: reply failed code=230001 msg=rate limited"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTenantAccessTokenInvalid(tt.err); got != tt.want {
				t.Fatalf("isTenantAccessTokenInvalid() = %v, want %v", got, tt.want)
			}
		})
	}
}

type testError string

func (e testError) Error() string { return string(e) }

func writeJSON(t *testing.T, w http.ResponseWriter, body map[string]any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Fatalf("encode json: %v", err)
	}
}
