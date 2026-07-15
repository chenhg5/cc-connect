package weixin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestBodyFromItemList_Text(t *testing.T) {
	got := bodyFromItemList([]messageItem{
		{Type: messageItemText, TextItem: &textItem{Text: "  hello  "}},
	})
	if got != "hello" {
		t.Fatalf("got %q", got)
	}
}

func TestBodyFromItemList_VoiceText(t *testing.T) {
	got := bodyFromItemList([]messageItem{
		{Type: messageItemVoice, VoiceItem: &voiceItem{Text: "transcribed"}},
	})
	if got != "transcribed" {
		t.Fatalf("got %q", got)
	}
}

func TestBodyFromItemList_Quote(t *testing.T) {
	ref := &refMessage{
		Title: "t",
		MessageItem: &messageItem{
			Type:     messageItemText,
			TextItem: &textItem{Text: "inner"},
		},
	}
	got := bodyFromItemList([]messageItem{
		{Type: messageItemText, TextItem: &textItem{Text: "reply"}, RefMsg: ref},
	})
	want := "[引用: t | inner]\nreply"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestSplitUTF8(t *testing.T) {
	s := string([]rune{'a', '啊', 'b', '吧', 'c'})
	parts := splitUTF8(s, 2)
	if len(parts) != 3 || parts[0] != "a啊" || parts[1] != "b吧" || parts[2] != "c" {
		t.Fatalf("parts=%#v", parts)
	}
}

func TestSplitUTF8Empty(t *testing.T) {
	parts := splitUTF8("", maxWeixinChunk)
	if len(parts) != 1 || parts[0] != "" {
		t.Fatalf("parts=%#v", parts)
	}
}

func TestMediaOnlyItems(t *testing.T) {
	if !mediaOnlyItems([]messageItem{{Type: messageItemImage}}) {
		t.Fatal("image should be media-only")
	}
	if mediaOnlyItems([]messageItem{{Type: messageItemVoice, VoiceItem: &voiceItem{Text: "x"}}}) {
		t.Fatal("voice with text is not media-only")
	}
}

func TestCollectInboundMediaUsesCDNHTTPClient(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/download" {
			t.Fatalf("path = %q, want /download", r.URL.Path)
		}
		if r.URL.Query().Get("encrypted_query_param") != "image-ref" {
			t.Fatalf("encrypted_query_param = %q, want image-ref", r.URL.Query().Get("encrypted_query_param"))
		}
		_, _ = w.Write(png)
	}))
	defer server.Close()

	p := &Platform{
		cdnBaseURL: server.URL,
		httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("api client should not download media")
		})},
		cdnHttpClient: server.Client(),
	}

	images, files, audio := p.collectInboundMedia(context.Background(), []messageItem{{
		Type: messageItemImage,
		ImageItem: &imageItem{
			Media: &cdnMedia{EncryptQueryParam: "image-ref"},
		},
	}})

	if len(images) != 1 {
		t.Fatalf("images len = %d, want 1", len(images))
	}
	if images[0].MimeType != "image/png" {
		t.Fatalf("mime = %q, want image/png", images[0].MimeType)
	}
	if string(images[0].Data) != string(png) {
		t.Fatalf("image data = %v, want %v", images[0].Data, png)
	}
	if len(files) != 0 {
		t.Fatalf("files len = %d, want 0", len(files))
	}
	if audio != nil {
		t.Fatalf("audio = %#v, want nil", audio)
	}
}

func TestSendMessageResp_JSON(t *testing.T) {
	var r sendMessageResp
	if err := json.Unmarshal([]byte(`{"ret":-1,"errcode":100,"errmsg":"rate limited"}`), &r); err != nil {
		t.Fatal(err)
	}
	if r.Ret != -1 || r.Errcode != 100 || r.Errmsg != "rate limited" {
		t.Fatalf("got %+v", r)
	}
}

func TestGetConfigReqUsesIlinkUserID(t *testing.T) {
	payload, err := json.Marshal(getConfigReq{
		IlinkUserID:  "peer@im.wechat",
		ContextToken: "ctx",
		BaseInfo:     baseInfo{ChannelVersion: channelVersion, BotAgent: defaultBotAgent},
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["ilink_user_id"] != "peer@im.wechat" {
		t.Fatalf("missing ilink_user_id in %s", payload)
	}
	if _, ok := decoded["user_id"]; ok {
		t.Fatalf("unexpected user_id in %s", payload)
	}
}

func TestAPIClientAddsIlinkHeadersAndBotAgent(t *testing.T) {
	var sent sendMessageReq
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("AuthorizationType"); got != "ilink_bot_token" {
			t.Fatalf("AuthorizationType = %q", got)
		}
		if got := r.Header.Get("iLink-App-Id"); got != ilinkAppID {
			t.Fatalf("iLink-App-Id = %q", got)
		}
		if got := r.Header.Get("iLink-App-ClientVersion"); got != ilinkAppClientVersion {
			t.Fatalf("iLink-App-ClientVersion = %q", got)
		}
		if got := r.Header.Get("X-WECHAT-UIN"); got == "" {
			t.Fatal("missing X-WECHAT-UIN")
		}
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer server.Close()

	client := newAPIClient(server.URL, "bearer", "", server.Client(), "MyBot/2.3.4 invalid")
	if err := client.sendText(context.Background(), "peer@im.wechat", "hello", "ctx", "cid"); err != nil {
		t.Fatal(err)
	}
	if sent.BaseInfo.ChannelVersion != channelVersion {
		t.Fatalf("channel_version = %q", sent.BaseInfo.ChannelVersion)
	}
	if sent.BaseInfo.BotAgent != "MyBot/2.3.4" {
		t.Fatalf("bot_agent = %q", sent.BaseInfo.BotAgent)
	}
}

func TestAPIClientAllowsContextFreeProactiveSend(t *testing.T) {
	var sent sendMessageReq
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client := newAPIClient(server.URL, "bearer", "", server.Client())
	if err := client.sendText(context.Background(), "peer@im.wechat", "hello", "", "cid"); err != nil {
		t.Fatal(err)
	}
	if sent.Msg.ContextToken != "" {
		t.Fatalf("context_token = %q, want omitted", sent.Msg.ContextToken)
	}
}

func TestPendingReplyPersistsLatestPerPeer(t *testing.T) {
	p := &Platform{pendingPath: filepath.Join(t.TempDir(), "pending_replies.json")}
	p.enqueuePendingReply("peer@im.wechat", "first", "send_failed")
	p.enqueuePendingReply("peer@im.wechat", "second", "missing_context_token")

	raw, err := os.ReadFile(p.pendingPath)
	if err != nil {
		t.Fatal(err)
	}
	var entries map[string]pendingReplyEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatal(err)
	}
	entry := entries["peer@im.wechat"]
	if entry.Content != "second" {
		t.Fatalf("content = %q, want latest", entry.Content)
	}
	if entry.Reason != "missing_context_token" {
		t.Fatalf("reason = %q", entry.Reason)
	}
}

func TestPendingReplyFlushUsesStoredContextToken(t *testing.T) {
	var sent sendMessageReq
	var sendCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ilink/bot/sendmessage" {
			t.Fatalf("path = %q, want /ilink/bot/sendmessage", r.URL.Path)
		}
		sendCount++
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer server.Close()

	p := &Platform{
		api:         newAPIClient(server.URL, "bearer", "", server.Client()),
		pendingPath: filepath.Join(t.TempDir(), "pending_replies.json"),
		tokens: map[string]contextTokenEntry{"peer@im.wechat": {
			Token:      "fresh-context-token",
			CapturedAt: time.Now().Format(time.RFC3339Nano),
		}},
	}
	p.enqueuePendingReply("peer@im.wechat", "hello", "send_failed")
	p.flushPendingReplies(context.Background(), false)

	if sendCount != 1 {
		t.Fatalf("sendCount = %d, want 1", sendCount)
	}
	if sent.Msg.ToUserID != "peer@im.wechat" {
		t.Fatalf("to_user_id = %q", sent.Msg.ToUserID)
	}
	if sent.Msg.ContextToken != "fresh-context-token" {
		t.Fatalf("context_token = %q", sent.Msg.ContextToken)
	}
	if len(sent.Msg.ItemList) != 1 || sent.Msg.ItemList[0].TextItem == nil || sent.Msg.ItemList[0].TextItem.Text == "" {
		t.Fatalf("unexpected item_list = %#v", sent.Msg.ItemList)
	}
	if _, err := os.Stat(p.pendingPath); !os.IsNotExist(err) {
		t.Fatalf("pending file still exists or stat failed: %v", err)
	}
}

func TestSendUsesLatestCachedContextTokenBeforeFirstAttempt(t *testing.T) {
	var sent sendMessageReq
	var sendCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ilink/bot/sendmessage" {
			t.Fatalf("path = %q, want /ilink/bot/sendmessage", r.URL.Path)
		}
		sendCount++
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer server.Close()

	p := &Platform{
		api: newAPIClient(server.URL, "bearer", "", server.Client()),
		tokens: map[string]contextTokenEntry{"peer@im.wechat": {
			Token:      "fresh-context-token",
			CapturedAt: time.Now().Format(time.RFC3339Nano),
		}},
	}
	rc := &replyContext{
		peerUserID:   "peer@im.wechat",
		contextToken: "stale-context-token",
		sessionKey:   "weixin:dm:peer@im.wechat",
	}

	if err := p.Send(context.Background(), rc, "hello"); err != nil {
		t.Fatal(err)
	}

	if sendCount != 1 {
		t.Fatalf("sendCount = %d, want 1", sendCount)
	}
	if sent.Msg.ContextToken != "fresh-context-token" {
		t.Fatalf("context_token = %q, want fresh-context-token", sent.Msg.ContextToken)
	}
	if rc.contextToken != "fresh-context-token" {
		t.Fatalf("reply context token = %q, want fresh-context-token", rc.contextToken)
	}
}

func TestSendAttemptsOlderContextTokenBeforeQueueing(t *testing.T) {
	var sendCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sendCount++
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer server.Close()

	p := &Platform{
		api:         newAPIClient(server.URL, "bearer", "", server.Client()),
		pendingPath: filepath.Join(t.TempDir(), "pending_replies.json"),
	}
	rc := &replyContext{
		peerUserID:             "peer@im.wechat",
		contextToken:           "old-context-token",
		contextTokenCapturedAt: time.Now().Add(-contextTokenFreshTTL - time.Second),
		sessionKey:             "weixin:dm:peer@im.wechat",
	}

	err := p.Send(context.Background(), rc, "hello after a long turn")
	if err != nil {
		t.Fatalf("err = %v, want send to be attempted and succeed", err)
	}
	if sendCount != 1 {
		t.Fatalf("sendCount = %d, want 1", sendCount)
	}
	if _, err := os.Stat(p.pendingPath); !os.IsNotExist(err) {
		t.Fatalf("pending file should not exist after successful send: %v", err)
	}
}

func TestSendRetMinus2FallsBackOnceWithoutContext(t *testing.T) {
	var sendCount int
	var tokens []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sendCount++
		var sent sendMessageReq
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, sent.Msg.ContextToken)
		if sendCount == 1 {
			_, _ = w.Write([]byte(`{"ret":-2,"errcode":0,"errmsg":""}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	now := time.Now()
	p := &Platform{
		api:         newAPIClient(server.URL, "bearer", "", server.Client()),
		pendingPath: filepath.Join(t.TempDir(), "pending_replies.json"),
		tokens: map[string]contextTokenEntry{"peer@im.wechat": {
			Token:      "same-context-token",
			CapturedAt: now.Format(time.RFC3339Nano),
		}},
	}
	rc := &replyContext{
		peerUserID:             "peer@im.wechat",
		contextToken:           "same-context-token",
		contextTokenCapturedAt: now,
		sessionKey:             "weixin:dm:peer@im.wechat",
	}

	if err := p.Send(context.Background(), rc, "hello"); err != nil {
		t.Fatal(err)
	}
	if sendCount != 2 {
		t.Fatalf("sendCount = %d, want contextual attempt plus one context-free fallback", sendCount)
	}
	if len(tokens) != 2 || tokens[0] != "same-context-token" || tokens[1] != "" {
		t.Fatalf("context tokens = %#v, want [same-context-token, empty]", tokens)
	}
	if !rc.deliveryUnconfirmed {
		t.Fatal("context-free acceptance must be marked delivery-unconfirmed")
	}
}

func TestSendRetMinus2DoesNotLoopAfterContextFreeFailure(t *testing.T) {
	var sendCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sendCount++
		_, _ = w.Write([]byte(`{"ret":-2,"errcode":0,"errmsg":""}`))
	}))
	defer server.Close()

	now := time.Now()
	p := &Platform{
		api:         newAPIClient(server.URL, "bearer", "", server.Client()),
		pendingPath: filepath.Join(t.TempDir(), "pending_replies.json"),
		tokens: map[string]contextTokenEntry{"peer@im.wechat": {
			Token:      "same-context-token",
			CapturedAt: now.Format(time.RFC3339Nano),
		}},
	}
	rc := &replyContext{
		peerUserID:             "peer@im.wechat",
		contextToken:           "same-context-token",
		contextTokenCapturedAt: now,
	}

	err := p.Send(context.Background(), rc, "hello")
	if err == nil || !containsStr(err.Error(), "ret=-2") {
		t.Fatalf("err = %v, want ret=-2", err)
	}
	if sendCount != 2 {
		t.Fatalf("sendCount = %d, want exactly two attempts", sendCount)
	}
}

func TestReconstructReplyContextAllowsMissingStoredToken(t *testing.T) {
	p := &Platform{tokens: map[string]contextTokenEntry{}}
	raw, err := p.ReconstructReplyCtx("weixin:dm:peer@im.wechat")
	if err != nil {
		t.Fatal(err)
	}
	rc, ok := raw.(*replyContext)
	if !ok {
		t.Fatalf("reply context type = %T", raw)
	}
	if rc.contextToken != "" || !rc.proactive {
		t.Fatalf("reply context = %#v, want proactive context without token", rc)
	}
}

func TestLoadTokensMigratesLegacyStringAsStale(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "context_tokens.json")
	if err := os.WriteFile(path, []byte(`{"peer@im.wechat":"legacy-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &Platform{tokensPath: path, accountLabel: "acct", tokens: map[string]contextTokenEntry{}}
	p.loadTokens()

	entry := p.getContextTokenEntry("peer@im.wechat")
	if entry.Token != "legacy-token" {
		t.Fatalf("token = %q", entry.Token)
	}
	if entry.fresh(time.Now()) {
		t.Fatal("legacy token without captured_at should be stale")
	}
}

func TestPendingReplyFlushSkipsExpiredSameTokenAfterCap(t *testing.T) {
	p := &Platform{}
	now := time.Now()
	entry := pendingReplyEntry{
		LastAttemptToken:  "ctx",
		TokenAttemptCount: pendingMaxAttemptsPerToken,
		LastAttemptAt:     now.Add(-time.Hour).Format(time.RFC3339),
	}
	if p.shouldAttemptPendingFlush(entry, "ctx", false, now) {
		t.Fatal("expected background flush to skip same token after cap")
	}
	if !p.shouldAttemptPendingFlush(entry, "new-ctx", false, now) {
		t.Fatal("expected background flush to try a new token")
	}
	if !p.shouldAttemptPendingFlush(entry, "ctx", true, now) {
		t.Fatal("expected forced flush to bypass same-token cap")
	}
}

func TestSendAudioRejectsEmptyAudio(t *testing.T) {
	p := &Platform{}
	// resolveReplyContext checks context_token first, so provide one
	rc := &replyContext{peerUserID: "test", contextToken: "valid-token", contextTokenCapturedAt: time.Now()}
	err := p.SendAudio(context.Background(), rc, []byte{}, "wav")
	if err == nil {
		t.Fatal("expected error for empty audio")
	}
	if !containsStr(err.Error(), "empty audio") {
		t.Fatalf("expected 'empty audio' error, got: %v", err)
	}
}

func TestSendAudioRejectsInvalidReplyContext(t *testing.T) {
	p := &Platform{}
	err := p.SendAudio(context.Background(), "invalid-context", []byte("audio-data"), "wav")
	if err == nil {
		t.Fatal("expected error for invalid reply context")
	}
	if !containsStr(err.Error(), "invalid reply context") {
		t.Fatalf("expected 'invalid reply context' error, got: %v", err)
	}
}

func TestSendAudioRejectsNilReplyContext(t *testing.T) {
	p := &Platform{}
	err := p.SendAudio(context.Background(), nil, []byte("audio-data"), "wav")
	if err == nil {
		t.Fatal("expected error for nil reply context")
	}
	if !containsStr(err.Error(), "invalid reply context") {
		t.Fatalf("expected 'invalid reply context' error, got: %v", err)
	}
}

func TestGetConfig_RejectsNonZeroErrcode(t *testing.T) {
	raw := `{"ret":0,"errcode":40001,"errmsg":"invalid token","typing_ticket":""}`
	var out getConfigResp
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if out.Errcode != 40001 {
		t.Fatalf("expected errcode 40001, got %d", out.Errcode)
	}
}

func TestGetConfig_RejectsNonZeroRet(t *testing.T) {
	raw := `{"ret":-1,"errcode":0,"errmsg":"internal error","typing_ticket":"tk"}`
	var out getConfigResp
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if out.Ret != -1 {
		t.Fatalf("expected ret -1, got %d", out.Ret)
	}
}

func TestGetConfigReq_JSONFieldName(t *testing.T) {
	req := getConfigReq{
		IlinkUserID: "test_user",
		BaseInfo:    baseInfo{ChannelVersion: "1.0"},
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"ilink_user_id"`) {
		t.Fatalf("expected ilink_user_id in JSON, got: %s", string(data))
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStrHelper(s, substr))
}

func containsStrHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// testLifecycleHandler captures lifecycle callbacks from a platform so tests
// can assert that OnPlatformReady is invoked at the right moment.
type testLifecycleHandler struct {
	mu          sync.Mutex
	readyCount  int32
	readyCh     chan struct{}
	unavailable []error
}

func newTestLifecycleHandler() *testLifecycleHandler {
	return &testLifecycleHandler{readyCh: make(chan struct{}, 1)}
}

func (h *testLifecycleHandler) OnPlatformReady(p core.Platform) {
	if atomic.AddInt32(&h.readyCount, 1) == 1 {
		h.readyCh <- struct{}{}
	}
}

func (h *testLifecycleHandler) OnPlatformUnavailable(p core.Platform, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.unavailable = append(h.unavailable, err)
}

func (h *testLifecycleHandler) ReadyCount() int {
	return int(atomic.LoadInt32(&h.readyCount))
}

// newILinkTestServer returns an httptest.Server that responds to ilink
// long-poll getUpdates calls with the provided body and status. Tests can
// inspect callCount to confirm pollLoop actually issued requests.
type ilinkTestServer struct {
	server    *httptest.Server
	callCount atomic.Int32
	body      string
	status    int
}

func newILinkTestServer(status int, body string) *ilinkTestServer {
	s := &ilinkTestServer{body: body, status: status}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	return s
}

func (s *ilinkTestServer) Close() { s.server.Close() }
func (s *ilinkTestServer) URL() string {
	return s.server.URL
}

func TestPollLoop_NotifiesReadyForPollAfterFirstSuccessfulGetUpdates(t *testing.T) {
	body := `{"ret":0,"errcode":0,"msgs":[],"get_updates_buf":"buf-1"}`
	srv := newILinkTestServer(http.StatusOK, body)
	defer srv.Close()

	p := &Platform{
		token:         "tok",
		baseURL:       srv.URL(),
		longPollMS:    100,
		accountLabel:  "default",
		httpClient:    &http.Client{},
		dedup:         make(map[string]time.Time),
		typingTickets: make(map[string]typingTicketEntry),
	}
	p.api = newAPIClient(srv.URL(), "tok", "", p.httpClient)

	handler := newTestLifecycleHandler()
	p.SetLifecycleHandler(handler)

	if err := p.Start(func(core.Platform, *core.Message) {}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop() })

	select {
	case <-handler.readyCh:
	case <-time.After(3 * time.Second):
		t.Fatalf("OnPlatformReady not observed within timeout (readyCount=%d, getUpdatesCalls=%d)",
			handler.ReadyCount(), srv.callCount.Load())
	}

	// Give pollLoop enough time to issue at least one more getUpdates; the
	// ready signal must remain a one-shot event.
	time.Sleep(400 * time.Millisecond)

	if got := handler.ReadyCount(); got != 1 {
		t.Fatalf("ready callbacks = %d, want exactly 1 (one-shot)", got)
	}
	if got := srv.callCount.Load(); got < 2 {
		t.Fatalf("getUpdates calls = %d, want >= 2 (pollLoop should keep polling)", got)
	}
}

func TestPollLoop_DoesNotNotifyReadyForPollWhileGetUpdatesFails(t *testing.T) {
	srv := newILinkTestServer(http.StatusInternalServerError, `{"ret":-1}`)
	defer srv.Close()

	p := &Platform{
		token:         "tok",
		baseURL:       srv.URL(),
		longPollMS:    100,
		accountLabel:  "default",
		httpClient:    &http.Client{},
		dedup:         make(map[string]time.Time),
		typingTickets: make(map[string]typingTicketEntry),
	}
	p.api = newAPIClient(srv.URL(), "tok", "", p.httpClient)

	handler := newTestLifecycleHandler()
	p.SetLifecycleHandler(handler)

	if err := p.Start(func(core.Platform, *core.Message) {}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop() })

	// While every getUpdates returns 500, the backoff grows (1s, 2s, 4s, …).
	// Within 2.5s we expect at least one more failed attempt but never a
	// ready-for-poll signal.
	time.Sleep(2500 * time.Millisecond)

	if got := handler.ReadyCount(); got != 0 {
		t.Fatalf("ready callbacks = %d, want 0 while getUpdates fails", got)
	}
	if got := srv.callCount.Load(); got < 1 {
		t.Fatalf("getUpdates calls = %d, want >= 1 (pollLoop should be retrying)", got)
	}
}

func TestPlatform_ImplementsAsyncRecoverablePlatform(t *testing.T) {
	var p core.Platform = &Platform{}
	if _, ok := p.(core.AsyncRecoverablePlatform); !ok {
		t.Fatal("weixin Platform must implement core.AsyncRecoverablePlatform so the engine waits for ready-for-poll")
	}
}

// TestContextToken_PersistAndReload verifies that context_token values written
// via setContextToken survive a process restart by being persisted to
// context_tokens.json and reloaded into the in-memory map on the next startup.
// This is the regression coverage for #1087.
func TestContextToken_PersistAndReload(t *testing.T) {
	dir := t.TempDir()
	tokensPath := filepath.Join(dir, "context_tokens.json")

	// 1. First "process": store two context_tokens, then verify the file exists
	//    with the expected JSON content.
	p1 := &Platform{
		tokens:     make(map[string]contextTokenEntry),
		tokensPath: tokensPath,
	}
	p1.setContextToken("user-aaa", "token-A", "msg-a", time.Now())
	p1.setContextToken("user-bbb", "token-B", "msg-b", time.Now())

	if _, err := os.Stat(tokensPath); err != nil {
		t.Fatalf("expected context_tokens.json at %s, got: %v", tokensPath, err)
	}

	// Confirm the on-disk format is a JSON object keyed by peer user ID.
	raw, err := os.ReadFile(tokensPath)
	if err != nil {
		t.Fatalf("read tokens file: %v", err)
	}
	var onDisk map[string]contextTokenEntry
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("parse tokens file: %v (raw=%q)", err, string(raw))
	}
	if onDisk["user-aaa"].Token != "token-A" || onDisk["user-bbb"].Token != "token-B" {
		t.Fatalf("on-disk tokens = %v, want user-aaa=token-A user-bbb=token-B", onDisk)
	}

	// 2. Second "process": same stateDir, fresh in-memory map. loadTokens()
	//    must read the file and populate the map.
	p2 := &Platform{
		tokens:     make(map[string]contextTokenEntry),
		tokensPath: tokensPath,
	}
	p2.loadTokens()

	if got := p2.getContextToken("user-aaa"); got != "token-A" {
		t.Errorf("after reload, user-aaa = %q, want %q", got, "token-A")
	}
	if got := p2.getContextToken("user-bbb"); got != "token-B" {
		t.Errorf("after reload, user-bbb = %q, want %q", got, "token-B")
	}

	// 3. ReconstructReplyCtx (the cron / cc-connect send path) must succeed
	//    using the reloaded token.
	rc, err := p2.ReconstructReplyCtx(sessionKeyPrefix + "user-aaa")
	if err != nil {
		t.Fatalf("ReconstructReplyCtx after reload: %v", err)
	}
	concrete, ok := rc.(*replyContext)
	if !ok {
		t.Fatalf("ReconstructReplyCtx returned %T, want *replyContext", rc)
	}
	if concrete.contextToken != "token-A" {
		t.Errorf("reloaded contextToken = %q, want %q", concrete.contextToken, "token-A")
	}
}

// TestContextToken_LoadMissingFile is a no-op fallback: if the persistence
// file does not exist (first run, or after a cleanup), loadTokens must not
// error and the in-memory map must remain empty.
func TestContextToken_LoadMissingFile(t *testing.T) {
	dir := t.TempDir()
	p := &Platform{
		tokens:     make(map[string]contextTokenEntry),
		tokensPath: filepath.Join(dir, "does-not-exist.json"),
	}
	// Should not panic, should not return an error.
	p.loadTokens()
	if got := p.getContextToken("anyone"); got != "" {
		t.Errorf("getContextToken on fresh state = %q, want empty", got)
	}
}

// TestReconstructReplyCtx_MissingToken verifies proactive reconstruction can
// proceed without a stored context token. The send path will use the explicit
// context-free fallback and mark delivery as unconfirmed.
func TestReconstructReplyCtx_MissingToken(t *testing.T) {
	p := &Platform{
		tokens: make(map[string]contextTokenEntry),
	}
	raw, err := p.ReconstructReplyCtx(sessionKeyPrefix + "never-messaged-user")
	if err != nil {
		t.Fatalf("ReconstructReplyCtx returned error: %v", err)
	}
	rc, ok := raw.(*replyContext)
	if !ok || !rc.proactive || rc.contextToken != "" {
		t.Fatalf("reply context = %#v, want proactive context without token", raw)
	}
}
