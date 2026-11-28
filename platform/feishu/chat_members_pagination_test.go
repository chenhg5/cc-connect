package feishu

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
)

// membersServer fakes the chat members API. Feishu answers the last page with
// has_more=false and an empty (not absent) page_token.
func membersServer(t *testing.T, pages []map[string]any) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success", "expire": 7200, "tenant_access_token": "t-token"})
		case strings.HasSuffix(r.URL.Path, "/members"):
			n := int(calls.Add(1))
			idx := 0
			if tok := r.URL.Query().Get("page_token"); tok != "" {
				idx = int(tok[len(tok)-1] - '0')
			}
			if idx >= len(pages) {
				idx = len(pages) - 1
			}
			_ = n
			writeJSON(t, w, pages[idx])
		default:
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success"})
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func membersPlatform(srv *httptest.Server) *Platform {
	const appID, appSecret = "cli_members", "secret-members"
	return &Platform{
		platformName: "feishu",
		domain:       srv.URL,
		appID:        appID,
		appSecret:    appSecret,
		client: lark.NewClient(appID, appSecret,
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
	}
}

func page(hasMore bool, token string, members ...[2]string) map[string]any {
	items := []map[string]any{}
	for _, m := range members {
		items = append(items, map[string]any{"member_id_type": "open_id", "member_id": m[1], "name": m[0]})
	}
	return map[string]any{"code": 0, "msg": "success", "data": map[string]any{
		"items": items, "has_more": hasMore, "page_token": token, "member_total": len(items),
	}}
}

func TestFetchChatMembers_LastPageEmptyTokenStops(t *testing.T) {
	srv, calls := membersServer(t, []map[string]any{
		page(false, "", [2]string{"Alice", "ou_a"}, [2]string{"Bob", "ou_b"}),
	})
	p := membersPlatform(srv)
	start := time.Now()
	got, err := p.fetchChatMembers(context.Background(), "oc_chat")
	if err != nil {
		t.Fatalf("fetchChatMembers error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("fetchChatMembers took %v, want a single quick request", elapsed)
	}
	if c := calls.Load(); c != 1 {
		t.Fatalf("members API called %d times, want 1", c)
	}
	if got["Alice"] != "ou_a" || got["Bob"] != "ou_b" || len(got) != 2 {
		t.Fatalf("members = %v", got)
	}
}

func TestFetchChatMembers_FollowsHasMore(t *testing.T) {
	srv, calls := membersServer(t, []map[string]any{
		page(true, "p1", [2]string{"Alice", "ou_a"}),
		page(false, "", [2]string{"Bob", "ou_b"}),
	})
	p := membersPlatform(srv)
	got, err := p.fetchChatMembers(context.Background(), "oc_chat")
	if err != nil {
		t.Fatalf("fetchChatMembers error: %v", err)
	}
	if c := calls.Load(); c != 2 {
		t.Fatalf("members API called %d times, want 2", c)
	}
	if got["Alice"] != "ou_a" || got["Bob"] != "ou_b" || len(got) != 2 {
		t.Fatalf("members = %v", got)
	}
}

func TestGetChatMembers_ErrorIsNotCached(t *testing.T) {
	srv, calls := membersServer(t, []map[string]any{
		{"code": 99991672, "msg": "Access denied"},
	})
	p := membersPlatform(srv)
	if m := p.getChatMembers(context.Background(), "oc_chat"); len(m) != 0 {
		t.Fatalf("members = %v, want empty on error", m)
	}
	p.getChatMembers(context.Background(), "oc_chat")
	if c := calls.Load(); c != 2 {
		t.Fatalf("members API called %d times, want 2 (failure must not be cached)", c)
	}
}
