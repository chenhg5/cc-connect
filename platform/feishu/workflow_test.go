package feishu

import (
	"context"
	"fmt"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestWorkflow_TaskThreadsRemainIsolatedWhenGroupSessionsShared(t *testing.T) {
	p, _ := newSplitCardFixture(t, false)
	p.shareSessionInChannel = true
	p.threadIsolation = false
	p.SetTaskThreadResolver(func(chat, root string) bool { return chat == "group" && root == "task-root" })
	group, root, other := "group", "task-root", "ordinary"
	msg := &larkim.EventMessage{ChatType: &group, RootId: &root}
	if key := p.makeSessionKey(msg, "group", "owner"); key != "feishu:group:root:task-root" {
		t.Fatalf("task mixed into group: %s", key)
	}
	msg.RootId = &other
	if key := p.makeSessionKey(msg, "group", "owner"); key != "feishu:group" {
		t.Fatalf("ordinary group behavior changed: %s", key)
	}
}

func TestWorkflowSourceLinksUseRealMessagePositions(t *testing.T) {
	for _, tt := range []struct {
		raw                string
		international      bool
		path, field, value string
	}{
		{`{"data":{"items":[{"chat_id":"oc_g","message_position":12}]}}`, false, "/client/chat/open", "position", "12"},
		{`{"data":{"items":[{"chat_id":"oc_g","thread_id":"omt_t","thread_message_position":"9"}]}}`, true, "/client/thread/open", "open_thread_id", "omt_t"},
	} {
		link, err := workflowSourceLink([]byte(tt.raw), tt.international)
		if err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(link)
		if err != nil || u.Path != tt.path || u.Query().Get(tt.field) != tt.value {
			t.Fatal("bad source link", link, err)
		}
		if tt.international && u.Host != "applink.larksuite.com" {
			t.Fatal("wrong brand")
		}
	}
	for _, raw := range []string{`{"data":{"items":[]}}`, `{"data":{"items":[{"chat_id":"g","deleted":true,"message_position":1}]}}`, `{"data":{"items":[{"chat_id":"g"}]}}`} {
		if _, err := workflowSourceLink([]byte(raw), false); err == nil {
			t.Fatal("invented a source link", raw)
		}
	}
}

func TestWorkflowSourceLinkThroughMessageAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "tenant_access_token") {
			fmt.Fprint(w, `{"code":0,"tenant_access_token":"test-token","expire":7200}`)
			return
		}
		if r.Method != "GET" || !strings.HasSuffix(r.URL.Path, "/messages/om_source") {
			t.Errorf("wrong request %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"code":0,"data":{"items":[{"message_id":"om_source","chat_id":"oc_group","message_position":4}]}}`)
	}))
	defer server.Close()
	p := &Platform{platformName: "feishu", appID: "cli_" + t.Name(), appSecret: "test"}
	p.client = lark.NewClient(p.appID, "test", lark.WithOpenBaseUrl(server.URL), lark.WithHttpClient(server.Client()))
	link, err := p.WorkflowMessageLink(context.Background(), "om_source")
	if err != nil || !strings.Contains(link, "position=4") {
		t.Fatal(link, err)
	}
}
