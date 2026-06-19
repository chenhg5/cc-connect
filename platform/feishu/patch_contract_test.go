package feishu

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
)

// TestFeishu_PatchHTTPContract verifies the HTTP contract of the Feishu Patch
// message API as exercised through Platform.UpdateMessage.
func TestFeishu_PatchHTTPContract(t *testing.T) {
	const appID = "cli_patch_contract"
	const appSecret = "secret"
	const messageID = "om_patch_contract"

	t.Run("200 OK", func(t *testing.T) {
		var method, path, authHeader string
		var body map[string]any

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/open-apis/auth/v3/tenant_access_token/internal":
				writeJSON(t, w, map[string]any{
					"code":                0,
					"msg":                 "success",
					"expire":              7200,
					"tenant_access_token": "valid-token",
				})
			default:
				method = r.Method
				path = r.URL.Path
				authHeader = r.Header.Get("Authorization")
				b, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(b, &body)
				writeJSON(t, w, map[string]any{"code": 0, "msg": "success"})
			}
		}))
		defer srv.Close()

		p := &Platform{
			platformName:       "feishu",
			domain:             srv.URL,
			appID:              appID,
			appSecret:          appSecret,
			useInteractiveCard: true,
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

		if err := p.UpdateMessage(context.Background(), &feishuPreviewHandle{messageID: messageID}, "hello"); err != nil {
			t.Fatalf("UpdateMessage() error = %v, want nil", err)
		}

		if method != http.MethodPatch {
			t.Errorf("PATCH method = %q, want PATCH", method)
		}
		if !strings.Contains(path, "/messages/"+messageID) {
			t.Errorf("PATCH path = %q, want to contain /messages/%s", path, messageID)
		}
		if authHeader != "Bearer valid-token" {
			t.Errorf("Authorization header = %q, want %q", authHeader, "Bearer valid-token")
		}
		content, ok := body["content"].(string)
		if !ok || content == "" {
			t.Fatalf("PATCH body content = %v, want non-empty string", body["content"])
		}
		if !strings.Contains(content, `"schema":"2.0"`) {
			t.Errorf("PATCH body content = %q, want card JSON with schema 2.0", content)
		}
	})

	t.Run("429 rate limit", func(t *testing.T) {
		var patchCalls atomic.Int32

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/open-apis/auth/v3/tenant_access_token/internal":
				writeJSON(t, w, map[string]any{
					"code":                0,
					"msg":                 "success",
					"expire":              7200,
					"tenant_access_token": "valid-token",
				})
			default:
				patchCalls.Add(1)
				w.WriteHeader(http.StatusTooManyRequests)
				writeJSON(t, w, map[string]any{"code": 99991465, "msg": "rate limited"})
			}
		}))
		defer srv.Close()

		p := &Platform{
			platformName:       "feishu",
			domain:             srv.URL,
			appID:              appID,
			appSecret:          appSecret,
			useInteractiveCard: true,
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

		if err := p.UpdateMessage(context.Background(), &feishuPreviewHandle{messageID: messageID}, "hello"); err == nil {
			t.Fatal("UpdateMessage() error = nil, want rate-limit error")
		}
		if patchCalls.Load() != 1 {
			t.Fatalf("PATCH calls = %d, want 1 (no retry on 429)", patchCalls.Load())
		}
	})

	t.Run("500 internal error", func(t *testing.T) {
		var patchCalls atomic.Int32

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/open-apis/auth/v3/tenant_access_token/internal":
				writeJSON(t, w, map[string]any{
					"code":                0,
					"msg":                 "success",
					"expire":              7200,
					"tenant_access_token": "valid-token",
				})
			default:
				patchCalls.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
				writeJSON(t, w, map[string]any{"code": 99991400, "msg": "internal server error"})
			}
		}))
		defer srv.Close()

		p := &Platform{
			platformName:       "feishu",
			domain:             srv.URL,
			appID:              appID,
			appSecret:          appSecret,
			useInteractiveCard: true,
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

		if err := p.UpdateMessage(context.Background(), &feishuPreviewHandle{messageID: messageID}, "hello"); err == nil {
			t.Fatal("UpdateMessage() error = nil, want 500 error")
		}
		if patchCalls.Load() != 1 {
			t.Fatalf("PATCH calls = %d, want 1 (no retry on 500)", patchCalls.Load())
		}
	})

	t.Run("timeout triggers transient retry", func(t *testing.T) {
		var patchCalls atomic.Int32
		shortClient := &http.Client{Timeout: 50 * time.Millisecond}

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/open-apis/auth/v3/tenant_access_token/internal":
				writeJSON(t, w, map[string]any{
					"code":                0,
					"msg":                 "success",
					"expire":              7200,
					"tenant_access_token": "valid-token",
				})
			default:
				patchCalls.Add(1)
				time.Sleep(200 * time.Millisecond)
				writeJSON(t, w, map[string]any{"code": 0, "msg": "success"})
			}
		}))
		defer srv.Close()

		p := &Platform{
			platformName:       "feishu",
			domain:             srv.URL,
			appID:              appID,
			appSecret:          appSecret,
			useInteractiveCard: true,
			client: lark.NewClient(appID, appSecret,
				lark.WithOpenBaseUrl(srv.URL),
				lark.WithHttpClient(shortClient),
			),
			replayClient: lark.NewClient(appID, appSecret,
				lark.WithEnableTokenCache(false),
				lark.WithOpenBaseUrl(srv.URL),
				lark.WithHttpClient(shortClient),
			),
		}

		if err := p.UpdateMessage(context.Background(), &feishuPreviewHandle{messageID: messageID}, "hello"); err == nil {
			t.Fatal("UpdateMessage() error = nil, want timeout error")
		}
		// initial attempt + 3 retries = 4 calls.
		if got := patchCalls.Load(); got < 2 || got > 4 {
			t.Fatalf("PATCH calls = %d, want between 2 and 4 (transient retries)", got)
		}
	})
}
