package feishu

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestPlatformRelayGroupVisibilityKey(t *testing.T) {
	p := &Platform{platformName: "feishu"}
	cases := []struct {
		name       string
		sessionKey string
		wantKey    string
		wantOK     bool
	}{
		{name: "root", sessionKey: "feishu:oc_chat:root:om_msg", wantKey: "feishu:oc_chat:root:om_msg", wantOK: true},
		{name: "thread", sessionKey: "feishu:oc_chat:thread:omt_msg", wantKey: "feishu:oc_chat:thread:omt_msg", wantOK: true},
		{name: "lark root", sessionKey: "lark:oc_chat:root:om_msg", wantKey: "lark:oc_chat:root:om_msg", wantOK: true},
		{name: "direct", sessionKey: "feishu:oc_chat:ou_user"},
		{name: "empty root", sessionKey: "feishu:oc_chat:root:"},
		{name: "foreign platform", sessionKey: "slack:C123:root:fake"},
		{name: "wrong configured platform", sessionKey: "lark:oc_chat:root:om_msg", wantOK: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "lark root" {
				p.platformName = "lark"
			} else {
				p.platformName = "feishu"
			}
			gotKey, gotOK := p.RelayGroupVisibilityKey(tc.sessionKey)
			if gotKey != tc.wantKey || gotOK != tc.wantOK {
				t.Fatalf("RelayGroupVisibilityKey(%q) = (%q, %v), want (%q, %v)", tc.sessionKey, gotKey, gotOK, tc.wantKey, tc.wantOK)
			}
		})
	}
}

func TestSendRelayGroupVisibilityKeepsTopicWhenReplyToTriggerDisabled(t *testing.T) {
	const rootMessageID = "om_root"
	var gotReplyInThread bool
	var gotMsgType string
	var createCalls int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{"code": 0, "expire": 7200, "tenant_access_token": "token"})
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/im/v1/messages/"+rootMessageID+"/reply":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read reply body: %v", err)
			}
			var req struct {
				MsgType       string `json:"msg_type"`
				ReplyInThread bool   `json:"reply_in_thread"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatalf("decode reply body: %v", err)
			}
			gotMsgType = req.MsgType
			gotReplyInThread = req.ReplyInThread
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success", "data": map[string]any{"message_id": "om_echo"}})
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/im/v1/messages":
			createCalls++
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success", "data": map[string]any{"message_id": "om_leaked"}})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	newClient := func() *lark.Client {
		return lark.NewClient("cli_test", "secret",
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		)
	}
	p := &Platform{
		platformName:     "feishu",
		domain:           srv.URL,
		appID:            "cli_test",
		appSecret:        "secret",
		noReplyToTrigger: true,
		client:           newClient(),
		replayClient:     newClient(),
	}

	if err := p.SendRelayGroupVisibility(context.Background(), "feishu:oc_chat:root:"+rootMessageID, "relay visible"); err != nil {
		t.Fatalf("SendRelayGroupVisibility() error = %v", err)
	}
	if !gotReplyInThread || gotMsgType != larkim.MsgTypeText {
		t.Fatalf("topic relay body = msg_type %q reply_in_thread %v", gotMsgType, gotReplyInThread)
	}
	if createCalls != 0 {
		t.Fatalf("relay visibility leaked to channel root via %d create calls", createCalls)
	}
}
