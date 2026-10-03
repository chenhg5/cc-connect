package feishu

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

type forwardMergeFixture struct {
	p               *Platform
	got             chan *core.Message
	lookups         atomic.Int32
	beforeLookup    func()
	withAttachments bool
	failLookup      bool
}

func newForwardMergeFixture(t *testing.T, window time.Duration) *forwardMergeFixture {
	t.Helper()
	f := &forwardMergeFixture{got: make(chan *core.Message, 16)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{"code": 0, "tenant_access_token": "test-token", "expire": 7200})
		case "/open-apis/im/v1/messages/om_A":
			f.lookups.Add(1)
			if f.beforeLookup != nil {
				f.beforeLookup()
			}
			if f.failLookup {
				writeJSON(t, w, map[string]any{"code": 230050, "msg": "test: unavailable"})
				return
			}
			items := []any{
				quotedForwardItem("om_A", "", "merge_forward", "", "ou_sender"),
				quotedForwardItem("om_child", "om_A", "text", `{"text":"forward material"}`, "ou_sender"),
			}
			if f.withAttachments {
				items = append(items,
					quotedForwardItem("om_image", "om_A", "image", `{"image_key":"img_test"}`, "ou_sender"),
					quotedForwardItem("om_file", "om_A", "file", `{"file_key":"file_test","file_name":"test.txt"}`, "ou_sender"))
			}
			writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"items": items}})
		case "/open-apis/im/v1/messages/om_A/resources/img_test":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
		case "/open-apis/im/v1/messages/om_file/resources/file_test":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("file material"))
		case "/open-apis/im/v1/messages/om_wrong":
			writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"items": []any{}}})
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	f.p = &Platform{
		platformName: "feishu", dedup: &core.MessageDedup{}, forwardMergeWindow: window,
		domain: srv.URL, appID: "forward-merge-test", appSecret: "test-secret", resourceDownloadHTTP: srv.Client(),
		client:  lark.NewClient("forward-merge-test", "test-secret", lark.WithOpenBaseUrl(srv.URL)),
		handler: func(_ core.Platform, msg *core.Message) { f.got <- msg },
	}
	f.p.userNameCache.Store("ou_sender", "Sender")
	f.p.userNameCache.Store("ou_other", "Other")
	f.p.chatNameCache.Store("oc_chat", "Chat")
	f.p.chatNameCache.Store("oc_other", "Other chat")
	t.Cleanup(func() {
		f.p.flushForwardMerges()
		f.p.messageDispatchMu.Lock()
		var tails []chan struct{}
		for _, tail := range f.p.messageDispatchTails {
			tails = append(tails, tail)
		}
		f.p.messageDispatchMu.Unlock()
		for _, tail := range tails {
			select {
			case <-tail:
			case <-time.After(3 * time.Second):
				t.Error("dispatch did not finish")
			}
		}
		srv.Close()
	})
	return f
}

func (f *forwardMergeFixture) event(id, kind, parent string, offset int64) *larkim.P2MessageReceiveV1 {
	e := dispatchOrderEvent(id, "oc_chat", kind, `{"text":"summarize it"}`, time.Now().Add(time.Second).UnixMilli()+offset)
	e.Event.Message.ParentId = strPtr(parent)
	return e
}

func (f *forwardMergeFixture) send(t *testing.T, e *larkim.P2MessageReceiveV1) {
	t.Helper()
	if err := f.p.onMessage(context.Background(), e); err != nil {
		t.Fatal(err)
	}
}

func TestOnMessageForwardMerge_ExplicitReplyAndNextTurn(t *testing.T) {
	for _, group := range []bool{false, true} {
		t.Run(map[bool]string{false: "private", true: "thread"}[group], func(t *testing.T) {
			f := newForwardMergeFixture(t, time.Second)
			f.withAttachments = true
			f.p.threadIsolation = group
			f.p.groupReplyAll = true
			a, b, c := f.event("om_A", "merge_forward", "", 0), f.event("om_B", "text", "om_A", 1), f.event("om_C", "text", "", 2)
			if group {
				for _, e := range []*larkim.P2MessageReceiveV1{a, b, c} {
					e.Event.Message.ChatType = strPtr("group")
					if e != a {
						e.Event.Message.RootId = strPtr("om_A")
					}
				}
			}
			f.send(t, a)
			f.send(t, b)
			f.send(t, b) // duplicate delivery cannot produce a second turn
			f.send(t, c) // ordinary follow-up remains separate
			merged := awaitGroupHistoryMessage(t, f.got)
			if merged.MessageID != "om_B" || merged.Content != "summarize it" || !strings.Contains(merged.ExtraContent, "forward material") {
				t.Fatalf("expected B + A material, got %+v", merged)
			}
			if merged.ReplyCtx.(replyContext).messageID != "om_B" {
				t.Fatal("reply must target B")
			}
			if len(merged.Images) != 1 || len(merged.Files) != 1 || string(merged.Files[0].Data) != "file material" {
				t.Fatal("merged input lost directly forwarded attachments")
			}
			if next := awaitGroupHistoryMessage(t, f.got); next.MessageID != "om_C" {
				t.Fatalf("next = %s", next.MessageID)
			}
			if f.lookups.Load() != 1 {
				t.Fatalf("lookups=%d, want one", f.lookups.Load())
			}
			select {
			case extra := <-f.got:
				t.Fatalf("extra turn: %s", extra.MessageID)
			default:
			}
		})
	}
}

func TestOnMessageForwardMerge_StandaloneAndLateReply(t *testing.T) {
	const window = 150 * time.Millisecond
	f := newForwardMergeFixture(t, window)
	started := time.Now()
	f.send(t, f.event("om_A", "merge_forward", "", 0))
	a := awaitGroupHistoryMessage(t, f.got)
	if a.MessageID != "om_A" || !strings.Contains(a.Content, "forward material") {
		t.Fatalf("standalone=%+v", a)
	}
	if time.Since(started) < window {
		t.Fatal("forward dispatched before window elapsed")
	}
	f.send(t, f.event("om_B", "text", "om_A", 1))
	if b := awaitGroupHistoryMessage(t, f.got); b.MessageID != "om_B" || !strings.Contains(b.ExtraContent, "forward material") {
		t.Fatalf("late reply=%+v", b)
	}
}

func TestOnMessageForwardMerge_Boundaries(t *testing.T) {
	for _, kind := range []string{"unquoted", "wrong parent", "foreign sender", "command", "intervening", "disabled"} {
		t.Run(kind, func(t *testing.T) {
			f := newForwardMergeFixture(t, time.Second)
			f.p.shareSessionInChannel = true
			if kind == "disabled" {
				f.p.forwardMergeWindow = 0
			}
			a, b := f.event("om_A", "merge_forward", "", 0), f.event("om_B", "text", "om_A", 2)
			switch kind {
			case "unquoted":
				b.Event.Message.ParentId = nil
			case "wrong parent":
				b.Event.Message.ParentId = strPtr("om_wrong")
			case "foreign sender":
				b.Event.Sender.SenderId.OpenId = strPtr("ou_other")
			case "command":
				b.Event.Message.Content = strPtr(`{"text":"/status"}`)
			}
			f.send(t, a)
			if kind == "intervening" {
				f.send(t, f.event("om_N", "text", "", 1))
			}
			f.send(t, b)
			if got := awaitGroupHistoryMessage(t, f.got); got.MessageID != "om_A" {
				t.Fatalf("A should stay separate, got %s", got.MessageID)
			}
			if kind == "intervening" {
				if got := awaitGroupHistoryMessage(t, f.got); got.MessageID != "om_N" {
					t.Fatalf("intervening=%s", got.MessageID)
				}
			}
			if got := awaitGroupHistoryMessage(t, f.got); got.MessageID != "om_B" {
				t.Fatalf("B=%s", got.MessageID)
			}
		})
	}
}

func TestOnMessageForwardMerge_SlowLookupDoesNotExtendDeadline(t *testing.T) {
	f := newForwardMergeFixture(t, 40*time.Millisecond)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.beforeLookup = func() { once.Do(func() { close(started); <-release }) }
	defer close(release)
	f.send(t, f.event("om_A", "merge_forward", "", 0))
	<-started
	time.Sleep(70 * time.Millisecond)
	f.send(t, f.event("om_B", "text", "om_A", 1))
	f.p.forwardMergeMu.Lock()
	batch := f.p.forwardMergePending["feishu:oc_chat:ou_sender"]
	f.p.forwardMergeMu.Unlock()
	if batch != nil {
		t.Fatal("late B must close, not join, the pending window")
	}
	// A different session remains independent of A's blocked API request.
	c := f.event("om_C", "text", "", 2)
	c.Event.Message.ChatId = strPtr("oc_other")
	f.send(t, c)
	if got := awaitGroupHistoryMessage(t, f.got); got.MessageID != "om_C" {
		t.Fatalf("other chat blocked: %s", got.MessageID)
	}
}

func TestOnMessageForwardMerge_RecallAndLookupFailure(t *testing.T) {
	for _, scenario := range []string{"recalled reply", "recalled forward", "lookup failure"} {
		t.Run(scenario, func(t *testing.T) {
			f := newForwardMergeFixture(t, time.Second)
			f.failLookup = scenario == "lookup failure"
			started, release := make(chan struct{}), make(chan struct{})
			var once, releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			f.beforeLookup = func() { once.Do(func() { close(started); <-release }) }
			f.send(t, f.event("om_A", "merge_forward", "", 0))
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("lookup did not start")
			}
			f.send(t, f.event("om_B", "text", "om_A", 1))
			want := "om_B"
			switch scenario {
			case "recalled reply":
				f.p.markMessageRecalled("om_B")
				want = "om_A"
			case "recalled forward":
				f.p.markMessageRecalled("om_A")
			}
			unblock()
			got := awaitGroupHistoryMessage(t, f.got)
			if got.MessageID != want {
				t.Fatalf("got %s, want %s", got.MessageID, want)
			}
			if scenario != "recalled reply" && got.ExtraContent != "" {
				t.Fatal("unavailable material must not be included")
			}
		})
	}
}

func TestNewPlatformForwardMergeWindow(t *testing.T) {
	for _, tc := range []struct {
		value any
		want  time.Duration
		bad   bool
	}{
		{nil, 0, false}, {0, 0, false}, {int64(1000), time.Second, false}, {-1, 0, true}, {10001, 0, true}, {"1000", 0, true},
	} {
		opts := map[string]any{"app_id": "test", "app_secret": "test"}
		if tc.value != nil {
			opts["forward_merge_window_ms"] = tc.value
		}
		p, err := newPlatform("feishu", lark.FeishuBaseUrl, opts)
		if tc.bad {
			if err == nil {
				t.Errorf("accepted invalid option %v", tc.value)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if got := extractBasePlatform(p).forwardMergeWindow; got != tc.want {
			t.Errorf("option %v: got %v want %v", tc.value, got, tc.want)
		}
	}
}
