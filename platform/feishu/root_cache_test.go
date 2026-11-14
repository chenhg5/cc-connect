package feishu

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

type rootStub struct {
	calls  int32
	rootOf func(string) (string, bool)
	// threadOf, when set, feeds thread_id into GET message responses.
	threadOf func(string) (string, bool)
	// threadMembers, when set, answers the thread message list used by the
	// mention-exemption fallback.
	threadMembers func(string) ([]map[string]any, bool)
	// omitCreateThreadID mimics Feishu's create response for a new topic root,
	// which carries no thread_id even though the message does belong to a topic.
	omitCreateThreadID bool
	slow               time.Duration
}

func newRootTestPlatform(t *testing.T, stub *rootStub, extraOpts ...map[string]any) *Platform {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success", "expire": 7200, "tenant_access_token": "token"})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/messages/"):
			if stub.slow > 0 {
				time.Sleep(stub.slow)
			}
			atomic.AddInt32(&stub.calls, 1)
			id := strings.TrimPrefix(r.URL.Path, "/open-apis/im/v1/messages/")
			rootID, ok := stub.rootOf(id)
			if !ok {
				w.WriteHeader(http.StatusInternalServerError)
				writeJSON(t, w, map[string]any{"code": 500, "msg": "boom"})
				return
			}
			item := map[string]any{"message_id": id, "root_id": rootID}
			if stub.threadOf != nil {
				if tid, has := stub.threadOf(id); has {
					item["thread_id"] = tid
				}
			}
			writeJSON(t, w, map[string]any{
				"code": 0, "msg": "success",
				"data": map[string]any{"items": []map[string]any{item}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/open-apis/im/v1/messages":
			items, ok := []map[string]any(nil), true
			if stub.threadMembers != nil {
				items, ok = stub.threadMembers(r.URL.Query().Get("container_id"))
			}
			if !ok {
				w.WriteHeader(http.StatusInternalServerError)
				writeJSON(t, w, map[string]any{"code": 500, "msg": "boom"})
				return
			}
			writeJSON(t, w, map[string]any{
				"code": 0, "msg": "success",
				"data": map[string]any{"items": items, "has_more": false},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/im/v1/messages":
			data := map[string]any{"message_id": "om_created"}
			if !stub.omitCreateThreadID {
				data["thread_id"] = "omt_new"
			}
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success", "data": data})
		default:
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success", "data": map[string]any{}})
		}
	}))
	t.Cleanup(srv.Close)

	opts := map[string]any{
		"app_id": "cli_test", "app_secret": "secret", "thread_isolation": true,
	}
	if len(extraOpts) > 0 {
		for k, v := range extraOpts[0] {
			opts[k] = v
		}
	}
	raw, err := newPlatform("feishu", srv.URL, opts)
	if err != nil {
		t.Fatalf("newPlatform() error = %v", err)
	}
	if ip, ok := raw.(*interactivePlatform); ok {
		return ip.Platform
	}
	return raw.(*Platform)
}

func TestCanonicalRootWalksToTop(t *testing.T) {
	stub := &rootStub{rootOf: func(id string) (string, bool) {
		switch id {
		case "om_m2":
			return "om_card", true
		case "om_card":
			return "", true
		}
		return "", false
	}}
	p := newRootTestPlatform(t, stub)

	if got := p.canonicalRoot(context.Background(), "om_m2"); got != "om_card" {
		t.Fatalf("canonicalRoot = %q, want om_card", got)
	}
	if n := atomic.LoadInt32(&stub.calls); n != 2 {
		t.Fatalf("lookups = %d, want 2", n)
	}
	// Second walk must be served from the cache.
	if got := p.canonicalRoot(context.Background(), "om_m2"); got != "om_card" {
		t.Fatalf("cached canonicalRoot = %q", got)
	}
	if n := atomic.LoadInt32(&stub.calls); n != 2 {
		t.Fatalf("lookups after cache = %d, want 2", n)
	}
}

func TestCanonicalRootUsesRecordedParent(t *testing.T) {
	stub := &rootStub{rootOf: func(string) (string, bool) { return "", false }}
	p := newRootTestPlatform(t, stub)
	p.rememberRoot("om_m2", "om_card")
	p.rememberRoot("om_card", "")

	if got := p.canonicalRoot(context.Background(), "om_m2"); got != "om_card" {
		t.Fatalf("canonicalRoot = %q, want om_card", got)
	}
	if n := atomic.LoadInt32(&stub.calls); n != 0 {
		t.Fatalf("lookups = %d, want 0", n)
	}
}

func TestCanonicalRootDegradesOnLookupError(t *testing.T) {
	stub := &rootStub{rootOf: func(string) (string, bool) { return "", false }}
	p := newRootTestPlatform(t, stub)

	if got := p.canonicalRoot(context.Background(), "om_x"); got != "om_x" {
		t.Fatalf("canonicalRoot = %q, want om_x", got)
	}
	if n := atomic.LoadInt32(&stub.calls); n != 1 {
		t.Fatalf("lookups = %d, want 1", n)
	}
	if _, ok := p.cachedRoot("om_x"); ok {
		t.Fatal("failed lookup must not be cached")
	}
	// A failed lookup must not be cached: the next walk retries.
	_ = p.canonicalRoot(context.Background(), "om_x")
	if n := atomic.LoadInt32(&stub.calls); n != 2 {
		t.Fatalf("lookups after retry = %d, want 2", n)
	}
}

func TestCanonicalRootStopsOnCycle(t *testing.T) {
	stub := &rootStub{rootOf: func(id string) (string, bool) {
		switch id {
		case "om_a":
			return "om_b", true
		case "om_b":
			return "om_a", true
		}
		return "", false
	}}
	p := newRootTestPlatform(t, stub)

	if got := p.canonicalRoot(context.Background(), "om_a"); got != "om_a" {
		t.Fatalf("canonicalRoot = %q, want om_a", got)
	}
	if n := atomic.LoadInt32(&stub.calls); n != 2 {
		t.Fatalf("lookups = %d, want 2", n)
	}
}

func TestCanonicalRootDepthLimit(t *testing.T) {
	stub := &rootStub{rootOf: func(id string) (string, bool) {
		n, err := strconv.Atoi(strings.TrimPrefix(id, "om_"))
		if err != nil || n >= 12 {
			return "", true
		}
		return "om_" + strconv.Itoa(n+1), true
	}}
	p := newRootTestPlatform(t, stub)

	if got := p.canonicalRoot(context.Background(), "om_0"); got != "om_10" {
		t.Fatalf("canonicalRoot = %q, want truncation at om_10", got)
	}
	if n := atomic.LoadInt32(&stub.calls); n != rootWalkMaxDepth {
		t.Fatalf("lookups = %d, want %d", n, rootWalkMaxDepth)
	}
}

func TestCanonicalRootConcurrentSingleflight(t *testing.T) {
	stub := &rootStub{
		slow:   60 * time.Millisecond,
		rootOf: func(string) (string, bool) { return "", true },
	}
	p := newRootTestPlatform(t, stub)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := p.canonicalRoot(context.Background(), "om_slow"); got != "om_slow" {
				t.Errorf("canonicalRoot = %q", got)
			}
		}()
	}
	wg.Wait()
	if n := atomic.LoadInt32(&stub.calls); n != 1 {
		t.Fatalf("lookups = %d, want 1", n)
	}
}

func TestRememberRootEvicts(t *testing.T) {
	p := &Platform{}
	for i := 0; i < rootCacheMax+1; i++ {
		p.rememberRoot("om_"+strconv.Itoa(i), "om_parent")
	}
	p.rootMu.Lock()
	n := len(p.rootCache)
	p.rootMu.Unlock()
	if want := rootCacheMax * 3 / 4; n != want {
		t.Fatalf("cache size = %d, want %d (75%%)", n, want)
	}
}

func TestMakeSessionKeyMergesTopic(t *testing.T) {
	p := &Platform{platformName: "feishu", threadIsolation: true}
	p.rememberRoot("om_m2", "om_card")
	p.rememberRoot("om_card", "")

	first := &larkim.EventMessage{
		ChatType:  stringPtr("group"),
		MessageId: stringPtr("om_m2"),
		RootId:    stringPtr("om_card"),
		ThreadId:  stringPtr("omt_1"),
	}
	followUp := &larkim.EventMessage{
		ChatType:  stringPtr("group"),
		MessageId: stringPtr("om_m3"),
		RootId:    stringPtr("om_m2"),
		ThreadId:  stringPtr("omt_1"),
	}

	k1 := p.makeSessionKey(context.Background(), first, "oc_1", "ou_a")
	k2 := p.makeSessionKey(context.Background(), followUp, "oc_1", "ou_b")
	if k1 != "feishu:oc_1:root:om_card" {
		t.Fatalf("first key = %q", k1)
	}
	if k2 != k1 {
		t.Fatalf("follow-up key = %q, want %q", k2, k1)
	}

	p2p := &larkim.EventMessage{ChatType: stringPtr("p2p"), MessageId: stringPtr("om_x")}
	if k := p.makeSessionKey(context.Background(), p2p, "oc_1", "ou_a"); k != "feishu:oc_1:ou_a" {
		t.Fatalf("p2p key = %q", k)
	}
}

func rootTestEvent(messageID, rootID, threadID, text string) *larkim.P2MessageReceiveV1 {
	chatID, chatType, senderType, openID, msgType, botID := "oc_test", "group", "user", "ou_test", "text", "ou_bot"
	content := `{"text":"` + text + `"}`
	createTime := strconv.FormatInt(time.Now().UnixMilli(), 10)
	return &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderId:   &larkim.UserId{OpenId: &openID},
				SenderType: &senderType,
			},
			Message: &larkim.EventMessage{
				MessageId:   &messageID,
				RootId:      &rootID,
				ThreadId:    &threadID,
				ChatId:      &chatID,
				ChatType:    &chatType,
				MessageType: &msgType,
				Content:     &content,
				CreateTime:  &createTime,
				Mentions:    []*larkim.MentionEvent{{Id: &larkim.UserId{OpenId: &botID}}},
			},
		},
	}
}

// rootTestEventUnmentioned is the same event without any mention, i.e. what a
// user types inside a topic without @-ing the bot.
func rootTestEventUnmentioned(messageID, rootID, threadID, text string) *larkim.P2MessageReceiveV1 {
	ev := rootTestEvent(messageID, rootID, threadID, text)
	ev.Event.Message.Mentions = nil
	return ev
}

func TestOnMessageRecordsInboundRoot(t *testing.T) {
	stub := &rootStub{rootOf: func(id string) (string, bool) {
		if id == "om_card" {
			return "", true
		}
		return "", false
	}}
	p := newRootTestPlatform(t, stub)
	p.botOpenID = "ou_bot"

	got := make(chan string, 1)
	p.handler = func(_ core.Platform, msg *core.Message) { got <- msg.SessionKey }

	if err := p.onMessage(context.Background(), rootTestEvent("om_m2", "om_card", "omt_1", "帮我看下")); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}
	if e, ok := p.cachedRoot("om_m2"); !ok || e.rootID != "om_card" {
		t.Fatalf("recorded root = %+v, ok = %v; want om_card", e, ok)
	}
	select {
	case key := <-got:
		if key != "feishu:oc_test:root:om_card" {
			t.Fatalf("session key = %q", key)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("message was not dispatched")
	}
	if n := atomic.LoadInt32(&stub.calls); n != 1 {
		t.Fatalf("lookups = %d, want 1", n)
	}
}

func TestCreateMessageDoesNotPrewarmRootCache(t *testing.T) {
	stub := &rootStub{rootOf: func(id string) (string, bool) {
		if id == "om_created" {
			return "", true
		}
		return "", false
	}}
	p := newRootTestPlatform(t, stub)

	if err := p.createMessage(context.Background(), "oc_test", "text", `{"text":"hi"}`, "test"); err != nil {
		t.Fatalf("createMessage() error = %v", err)
	}
	if _, ok := p.cachedRoot("om_created"); ok {
		t.Fatal("unreferenced bot message must not be cached")
	}
	if n := atomic.LoadInt32(&stub.calls); n != 0 {
		t.Fatalf("lookups after create = %d, want 0", n)
	}

	// The first real reference performs one lookup and records the head.
	if got := p.canonicalRoot(context.Background(), "om_created"); got != "om_created" {
		t.Fatalf("canonicalRoot = %q", got)
	}
	if e, ok := p.cachedRoot("om_created"); !ok || e.rootID != "" {
		t.Fatalf("cached root = %+v, ok = %v; want top", e, ok)
	}
	if n := atomic.LoadInt32(&stub.calls); n != 1 {
		t.Fatalf("lookups after first reference = %d, want 1", n)
	}
	_ = p.canonicalRoot(context.Background(), "om_created")
	if n := atomic.LoadInt32(&stub.calls); n != 1 {
		t.Fatalf("lookups after cached reference = %d, want 1", n)
	}
}

func TestRootLookupBreakerOpens(t *testing.T) {
	stub := &rootStub{rootOf: func(string) (string, bool) { return "", false }}
	p := newRootTestPlatform(t, stub)

	for i := 0; i < rootBreakerThreshold; i++ {
		if got := p.canonicalRoot(context.Background(), "om_x"); got != "om_x" {
			t.Fatalf("canonicalRoot = %q, want om_x", got)
		}
	}
	if n := atomic.LoadInt32(&stub.calls); n != rootBreakerThreshold {
		t.Fatalf("lookups = %d, want %d", n, rootBreakerThreshold)
	}
	// Breaker is open: further walks must fail fast without touching Feishu.
	if got := p.canonicalRoot(context.Background(), "om_x"); got != "om_x" {
		t.Fatalf("canonicalRoot with open breaker = %q", got)
	}
	if n := atomic.LoadInt32(&stub.calls); n != rootBreakerThreshold {
		t.Fatalf("lookups after breaker opened = %d, want %d", n, rootBreakerThreshold)
	}
}

func TestRootLookupBreakerRecoversAfterProbe(t *testing.T) {
	var fail int32 = 1
	stub := &rootStub{rootOf: func(string) (string, bool) {
		return "", atomic.LoadInt32(&fail) == 0
	}}
	p := newRootTestPlatform(t, stub)

	for i := 0; i < rootBreakerThreshold; i++ {
		_ = p.canonicalRoot(context.Background(), "om_x")
	}
	before := atomic.LoadInt32(&stub.calls)

	// Simulate the cooldown elapsing, then let the probe succeed.
	p.rootMu.Lock()
	p.rootBrokenUntil = time.Now().Add(-time.Second)
	p.rootMu.Unlock()
	atomic.StoreInt32(&fail, 0)

	if got := p.canonicalRoot(context.Background(), "om_probe"); got != "om_probe" {
		t.Fatalf("canonicalRoot = %q", got)
	}
	if n := atomic.LoadInt32(&stub.calls); n != before+1 {
		t.Fatalf("probe lookups = %d, want %d", n, before+1)
	}
	p.rootMu.Lock()
	broken := !p.rootBrokenUntil.IsZero()
	streak := p.rootFailStreak
	p.rootMu.Unlock()
	if broken || streak != 0 {
		t.Fatalf("breaker not reset: broken=%v streak=%d", broken, streak)
	}
}

// Several people @ the bot on the same alert at the same time, each opening
// their own topic. All of them must resolve to one session with one lookup.
func TestOnMessageConcurrentMentionsShareTopicSession(t *testing.T) {
	stub := &rootStub{rootOf: func(id string) (string, bool) {
		if id == "om_card" {
			return "", true
		}
		return "", false
	}}
	p := newRootTestPlatform(t, stub)
	p.botOpenID = "ou_bot"

	const n = 10
	keys := make(chan string, n)
	p.handler = func(_ core.Platform, msg *core.Message) { keys <- msg.SessionKey }

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ev := rootTestEvent("om_m"+strconv.Itoa(i), "om_card", "omt_"+strconv.Itoa(i), "看下这条告警")
			if err := p.onMessage(context.Background(), ev); err != nil {
				t.Errorf("onMessage(%d) error = %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	seen := make(map[string]int)
	for i := 0; i < n; i++ {
		select {
		case k := <-keys:
			seen[k]++
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d/%d messages dispatched", i, n)
		}
	}
	if len(seen) != 1 {
		t.Fatalf("session keys = %v, want exactly one", seen)
	}
	if _, ok := seen["feishu:oc_test:root:om_card"]; !ok {
		t.Fatalf("unexpected keys = %v", seen)
	}
	if got := atomic.LoadInt32(&stub.calls); got != 1 {
		t.Fatalf("lookups = %d, want 1", got)
	}
}

// Only the topic head is worth recording. A deeper reply is never somebody
// else's root, so it must not grow the cache.
func TestOnMessageSecondMessageInThreadIsNotRecorded(t *testing.T) {
	stub := &rootStub{rootOf: func(id string) (string, bool) {
		switch id {
		case "om_card":
			return "", true
		case "om_m2":
			return "om_card", true
		}
		return "", false
	}}
	p := newRootTestPlatform(t, stub)
	p.botOpenID = "ou_bot"

	keys := make(chan string, 2)
	p.handler = func(_ core.Platform, msg *core.Message) { keys <- msg.SessionKey }

	if err := p.onMessage(context.Background(), rootTestEvent("om_m2", "om_card", "omt_1", "帮我看下")); err != nil {
		t.Fatalf("onMessage(M2) error = %v", err)
	}
	if err := p.onMessage(context.Background(), rootTestEvent("om_m3", "om_m2", "omt_1", "是不是75949618")); err != nil {
		t.Fatalf("onMessage(M3) error = %v", err)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-keys:
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d/2 messages dispatched", i)
		}
	}

	if e, ok := p.cachedRoot("om_m2"); !ok || e.rootID != "om_card" {
		t.Fatalf("topic head not recorded: %+v ok=%v", e, ok)
	}
	if _, ok := p.cachedRoot("om_m3"); ok {
		t.Fatal("deeper reply must not be recorded")
	}
	if e, ok := p.cachedRoot("om_card"); !ok || e.rootID != "" {
		t.Fatalf("alert card not recorded as head: %+v ok=%v", e, ok)
	}
	p.rootMu.Lock()
	cacheSize := len(p.rootCache)
	p.rootMu.Unlock()
	if cacheSize != 2 {
		t.Fatalf("cache size = %d, want only topic head and alert card", cacheSize)
	}
	if n := atomic.LoadInt32(&stub.calls); n != 1 {
		t.Fatalf("lookups = %d, want 1 (the alert card only)", n)
	}
}

// After a restart the first message seen in an old topic is not the topic
// head. It must not be recorded, or the cache fills with dead entries again.
func TestOnMessageLateJoinDoesNotRecordTopicMessage(t *testing.T) {
	stub := &rootStub{rootOf: func(id string) (string, bool) {
		switch id {
		case "om_l":
			return "om_card", true
		case "om_card":
			return "", true
		}
		return "", false
	}}
	p := newRootTestPlatform(t, stub)
	p.botOpenID = "ou_bot"

	keys := make(chan string, 1)
	p.handler = func(_ core.Platform, msg *core.Message) { keys <- msg.SessionKey }

	if err := p.onMessage(context.Background(), rootTestEvent("om_x", "om_l", "omt_old", "重启后第一条")); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}
	select {
	case key := <-keys:
		if key != "feishu:oc_test:root:om_card" {
			t.Fatalf("session key = %q", key)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("message was not dispatched")
	}
	if _, ok := p.cachedRoot("om_x"); ok {
		t.Fatal("a late-join topic message must not be recorded")
	}
	if e, ok := p.cachedRoot("om_l"); !ok || e.rootID != "om_card" {
		t.Fatalf("walked ancestor not cached: %+v ok=%v", e, ok)
	}
	if e, ok := p.cachedRoot("om_card"); !ok || e.rootID != "" {
		t.Fatalf("alert card not cached as head: %+v ok=%v", e, ok)
	}
	p.rootMu.Lock()
	cacheSize := len(p.rootCache)
	p.rootMu.Unlock()
	if cacheSize != 2 {
		t.Fatalf("cache size = %d, want only walked ancestors", cacheSize)
	}
	if n := atomic.LoadInt32(&stub.calls); n != 2 {
		t.Fatalf("lookups = %d, want 2 (topic head + card)", n)
	}
}

// A brand-new message is already known to be a chain head. Resolve it
// directly without querying Feishu or speculatively growing the cache.
func TestOnMessageNewMessageSkipsCacheAndLookup(t *testing.T) {
	stub := &rootStub{rootOf: func(string) (string, bool) { return "", false }}
	p := newRootTestPlatform(t, stub)
	p.botOpenID = "ou_bot"

	keys := make(chan string, 1)
	p.handler = func(_ core.Platform, msg *core.Message) { keys <- msg.SessionKey }

	if err := p.onMessage(context.Background(), rootTestEvent("om_new", "", "", "帮我看下")); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}
	select {
	case key := <-keys:
		if key != "feishu:oc_test:root:om_new" {
			t.Fatalf("session key = %q", key)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("message was not dispatched")
	}
	if _, ok := p.cachedRoot("om_new"); ok {
		t.Fatal("unreferenced new message must not be cached")
	}
	if n := atomic.LoadInt32(&stub.calls); n != 0 {
		t.Fatalf("lookups = %d, want 0", n)
	}
}
