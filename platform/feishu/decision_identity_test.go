package feishu

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func TestDecisionRequesterTranslatedBetweenApplications(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			source, _ := newSplitCardFixture(t, true, map[string]any{"app_id": "manager-app"})
			target, _ := newSplitCardFixture(t, true, map[string]any{"app_id": "member-app"})
			managerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
					fmt.Fprint(w, `{"code":0,"tenant_access_token":"manager-token","expire":7200}`)
					return
				}
				if r.URL.Path != "/open-apis/contact/v3/users/ou_manager_view" || r.URL.Query().Get("user_id_type") != "open_id" {
					t.Errorf("wrong source lookup: %s", r.URL)
				}
				if missing {
					fmt.Fprint(w, `{"code":0,"data":{"user":{"open_id":"ou_manager_view"}}}`)
					return
				}
				fmt.Fprint(w, `{"code":0,"data":{"user":{"open_id":"ou_manager_view","union_id":"on_same_person"}}}`)
			}))
			defer managerServer.Close()
			memberServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
					fmt.Fprint(w, `{"code":0,"tenant_access_token":"member-token","expire":7200}`)
					return
				}
				if r.URL.Path != "/open-apis/contact/v3/users/on_same_person" || r.URL.Query().Get("user_id_type") != "union_id" {
					t.Errorf("wrong member lookup: %s", r.URL)
				}
				fmt.Fprint(w, `{"code":0,"data":{"user":{"open_id":"ou_member_view","union_id":"on_same_person"}}}`)
			}))
			defer memberServer.Close()
			source.client = lark.NewClient(source.appID, "test", lark.WithOpenBaseUrl(managerServer.URL), lark.WithHttpClient(managerServer.Client()))
			target.client = lark.NewClient(target.appID, "test", lark.WithOpenBaseUrl(memberServer.URL), lark.WithHttpClient(memberServer.Client()))
			resolver, ok := any(target).(interface {
				ResolveDecisionUser(context.Context, core.Platform, string) (string, error)
			})
			if !ok {
				t.Fatal("member platform cannot translate manager-scoped requester identity")
			}
			got, err := resolver.ResolveDecisionUser(context.Background(), source, "ou_manager_view")
			if missing {
				if err == nil || got != "" {
					t.Fatal("unverified identity accepted")
				}
				return
			}
			if err != nil || got != "ou_member_view" {
				t.Fatalf("requester=%q err=%v", got, err)
			}
			if same, err := resolver.ResolveDecisionUser(context.Background(), target, "ou_member_view"); err != nil || same != "ou_member_view" {
				t.Fatal("same-app identity changed", err)
			}
		})
	}
}
