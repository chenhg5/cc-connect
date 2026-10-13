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

func TestTeamGroup_AuthenticatedCreateAndVerifyWithIdempotencyAndPartialFailure(t *testing.T) {
	const name = "商城 Web API"
	const appID = "cli_project_group_auth"
	partial := false
	creates := 0
	adds := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/open-apis/auth/v3/tenant_access_token/internal" && r.Header.Get("Authorization") != "Bearer test-token" {
			w.Write([]byte(`{"code":99991661,"msg":"Missing access token for authorization"}`))
			return
		}
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			w.Write([]byte(`{"code":0,"tenant_access_token":"test-token","expire":7200}`))
		case "/open-apis/im/v1/chats":
			creates++
			if r.URL.Query().Get("uuid") != "operation-1" {
				t.Error("missing idempotency key")
			}
			var b struct {
				Name  string   `json:"name"`
				Users []string `json:"user_id_list"`
			}
			json.NewDecoder(r.Body).Decode(&b)
			if b.Name != name || len(b.Users) != 1 || b.Users[0] != "ou_creator" {
				t.Error("wrong group participants")
			}
			w.Write([]byte(`{"code":0,"data":{"chat_id":"oc_shop"}}`))
		case "/open-apis/im/v1/chats/oc_shop/members":
			adds++
			if r.URL.Query().Get("member_id_type") != "app_id" {
				t.Error("bots require app ids")
			}
			if partial {
				w.Write([]byte(`{"code":0,"data":{"pending_approval_id_list":["cli_dev"]}}`))
			} else {
				w.Write([]byte(`{"code":0,"data":{}}`))
			}
		case "/open-apis/im/v1/chats/oc_shop/members/is_in_chat":
			w.Write([]byte(`{"code":0,"data":{"is_in_chat":true}}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	p := &Platform{
		appID: appID, appSecret: "test", platformName: "feishu",
		client:       lark.NewClient(appID, "test", lark.WithOpenBaseUrl(server.URL)),
		replayClient: lark.NewClient(appID, "test", lark.WithOpenBaseUrl(server.URL), lark.WithEnableTokenCache(false)),
	}
	id, err := p.CreateProjectGroup(context.Background(), name, "ou_creator", "operation-1")
	if err != nil || id != "oc_shop" {
		t.Fatal(id, err)
	}
	if err = p.EnsureProjectGroupBots(context.Background(), id, []string{appID, "cli_dev"}); err != nil {
		t.Fatal(err)
	}
	present, err := p.ProjectGroupHasBot(context.Background(), id, appID)
	if err != nil || !present {
		t.Fatal(present, err)
	}
	partial = true
	if p.EnsureProjectGroupBots(context.Background(), id, []string{"cli_dev"}) == nil {
		t.Fatal("pending bot reported ready")
	}
	if creates != 1 || adds != 2 {
		t.Fatal("unexpected calls")
	}
}

func TestTeamGroup_TenantTokenRefreshAndAPIErrors(t *testing.T) {
	operations := []struct {
		name string
		path string
		run  func(*testing.T, *Platform) error
	}{
		{"create", "/open-apis/im/v1/chats", func(t *testing.T, p *Platform) error {
			id, err := p.CreateProjectGroup(context.Background(), "商城 Web API", "ou_creator", "operation-retry")
			if err == nil && id != "oc_shop" {
				t.Errorf("chat ID = %q, want oc_shop", id)
			}
			return err
		}},
		{"add", "/open-apis/im/v1/chats/oc_shop/members", func(t *testing.T, p *Platform) error {
			return p.EnsureProjectGroupBots(context.Background(), "oc_shop", []string{"cli_dev"})
		}},
		{"check", "/open-apis/im/v1/chats/oc_shop/members/is_in_chat", func(t *testing.T, p *Platform) error {
			present, err := p.ProjectGroupHasBot(context.Background(), "oc_shop", p.appID)
			if err == nil && !present {
				t.Error("manager should be present")
			}
			return err
		}},
	}
	for _, op := range operations {
		t.Run(op.name, func(t *testing.T) {
			for _, scenario := range []string{"expired_token", "api_error"} {
				t.Run(scenario, func(t *testing.T) {
					authCalls, apiCalls := 0, 0
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "application/json")
						if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
							authCalls++
							token := "stale-token"
							if authCalls > 1 {
								token = "fresh-token"
							}
							writeJSON(t, w, map[string]any{"code": 0, "tenant_access_token": token, "expire": 7200})
							return
						}
						if r.URL.Path != op.path {
							t.Errorf("unexpected path %s", r.URL.Path)
							w.WriteHeader(http.StatusNotFound)
							return
						}
						apiCalls++
						if op.name == "create" {
							var body struct {
								Name string `json:"name"`
							}
							if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
								t.Error(err)
							}
							if r.URL.Query().Get("uuid") != "operation-retry" || body.Name != "商城 Web API" {
								t.Error("retry changed the project name or idempotency key")
							}
						}
						authz := r.Header.Get("Authorization")
						if authz != "Bearer stale-token" && authz != "Bearer fresh-token" {
							t.Error("request has no valid tenant authorization")
							writeJSON(t, w, map[string]any{"code": 99991661, "msg": "Missing access token for authorization"})
							return
						}
						if scenario == "api_error" {
							writeJSON(t, w, map[string]any{"code": 230001, "msg": "invalid request; app_secret=test-secret"})
							return
						}
						if authz == "Bearer stale-token" {
							writeJSON(t, w, map[string]any{"code": 99991663, "msg": "Invalid access token for authorization"})
							return
						}
						writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"chat_id": "oc_shop", "is_in_chat": true}})
					}))
					defer server.Close()
					appID := "cli_" + strings.ReplaceAll(t.Name(), "/", "_")
					p := &Platform{
						appID: appID, appSecret: "test-secret", platformName: "feishu",
						client:       lark.NewClient(appID, "test-secret", lark.WithOpenBaseUrl(server.URL)),
						replayClient: lark.NewClient(appID, "test-secret", lark.WithOpenBaseUrl(server.URL), lark.WithEnableTokenCache(false)),
					}
					err := op.run(t, p)
					if scenario == "api_error" {
						if err == nil || !strings.Contains(err.Error(), "230001") || !strings.Contains(err.Error(), "invalid request") || strings.Contains(err.Error(), "test-secret") {
							t.Fatalf("error = %v, want code and redacted API reason", err)
						}
						if authCalls != 1 || apiCalls != 1 {
							t.Fatalf("auth/API calls = %d/%d, want 1/1 for non-token failure", authCalls, apiCalls)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					// The SDK retries the cached token once; the platform then fetches a fresh token.
					if authCalls != 2 || apiCalls != 3 {
						t.Fatalf("auth/API calls = %d/%d, want 2/3", authCalls, apiCalls)
					}
				})
			}
		})
	}
}
