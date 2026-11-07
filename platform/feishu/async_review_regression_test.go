package feishu

import (
	"fmt"
	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAsyncReview_SharedCardDispatchStopsAfterOwner(t *testing.T) {
	for _, ownerFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(ownerFirst), func(t *testing.T) {
			a, ma := newAllowFromTestPlatform(t, "ou_allowed")
			b, mb := newAllowFromTestPlatform(t, "ou_allowed")
			for _, p := range []*interactivePlatform{a, b} {
				p.userNameCache.Store("ou_allowed", "Test user")
				p.chatNameCache.Store("oc_test_chat", "Test chat")
			}
			a.SetAsyncQuestionOwner(func(key, id string) bool { return key == "feishu:oc_test_chat:ou_allowed" && id == "cTE" })
			b.SetAsyncQuestionOwner(func(string, string) bool { return false })
			platforms := []*Platform{a.Platform, b.Platform}
			if !ownerFirst {
				platforms[0], platforms[1] = platforms[1], platforms[0]
			}
			group := &sharedWSGroup{platforms: platforms}
			a.sharedGroup = group
			b.sharedGroup = group
			resp, err := b.dispatchSharedCardAction(cardActionEvent("ou_allowed", "askq:cTE:0:2"))
			if err != nil {
				t.Fatal(err)
			}
			waitAnyMessage(t, ma, time.Second, "owner")
			waitNoMessage(t, mb, 100*time.Millisecond, "sibling")
			if resp == nil || resp.Card != nil {
				t.Fatal("want handled acknowledgement without optimistic card")
			}
			a.cardActionMsgMu.Lock()
			tracked := a.cardActionMsgIDs["feishu:oc_test_chat:ou_allowed"]
			a.cardActionMsgMu.Unlock()
			if tracked != "om_test_message" {
				t.Fatalf("tracked card=%q", tracked)
			}
		})
	}
}

func TestAsyncReview_FailedCardPatchFallsBackToText(t *testing.T) {
	var mu sync.Mutex
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "tenant_access_token"):
			_, _ = io.WriteString(w, `{"code":0,"expire":7200,"tenant_access_token":"test-token"}`)
		case r.Method == http.MethodPatch:
			_, _ = io.WriteString(w, `{"code":234001,"msg":"card no longer editable"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/im/v1/messages":
			raw, _ := io.ReadAll(r.Body)
			mu.Lock()
			sent = append(sent, string(raw))
			mu.Unlock()
			_, _ = io.WriteString(w, `{"code":0,"data":{"message_id":"feedback"}}`)
		default:
			_, _ = io.WriteString(w, `{"code":0}`)
		}
	}))
	defer srv.Close()
	p, messages := newAllowFromTestPlatform(t, "ou_allowed")
	p.userNameCache.Store("ou_allowed", "Test user")
	p.chatNameCache.Store("oc_test_chat", "Test chat")
	p.domain = srv.URL
	p.client = lark.NewClient("review-app", "secret", lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client()))
	_, err := p.onCardAction(cardActionEvent("ou_allowed", "askq:cTE:0:2"))
	if err != nil {
		t.Fatal(err)
	}
	msg := waitAnyMessage(t, messages, time.Second, "answer")
	msg.OnAnswerStatus(core.NewCard().Title("Answer submitted", "green").Build())
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 1 || !strings.Contains(sent[0], "Answer submitted") {
		t.Fatalf("fallback messages=%v", sent)
	}
}
