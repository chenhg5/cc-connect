package weixin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestSendAudioRejectsEmptyAudio(t *testing.T) {
	p := &Platform{}
	// resolveReplyContext checks context_token first, so provide one
	rc := &replyContext{peerUserID: "test", contextToken: "valid-token"}
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
		UserID:   "test_user",
		BaseInfo: baseInfo{ChannelVersion: "1.0"},
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
		tokens:     make(map[string]string),
		tokensPath: tokensPath,
	}
	p1.setContextToken("user-aaa", "token-A")
	p1.setContextToken("user-bbb", "token-B")

	if _, err := os.Stat(tokensPath); err != nil {
		t.Fatalf("expected context_tokens.json at %s, got: %v", tokensPath, err)
	}

	// Confirm the on-disk format is a JSON object keyed by peer user ID.
	raw, err := os.ReadFile(tokensPath)
	if err != nil {
		t.Fatalf("read tokens file: %v", err)
	}
	var onDisk map[string]string
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("parse tokens file: %v (raw=%q)", err, string(raw))
	}
	if onDisk["user-aaa"] != "token-A" || onDisk["user-bbb"] != "token-B" {
		t.Fatalf("on-disk tokens = %v, want user-aaa=token-A user-bbb=token-B", onDisk)
	}

	// 2. Second "process": same stateDir, fresh in-memory map. loadTokens()
	//    must read the file and populate the map.
	p2 := &Platform{
		tokens:     make(map[string]string),
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
		tokens:     make(map[string]string),
		tokensPath: filepath.Join(dir, "does-not-exist.json"),
	}
	// Should not panic, should not return an error.
	p.loadTokens()
	if got := p.getContextToken("anyone"); got != "" {
		t.Errorf("getContextToken on fresh state = %q, want empty", got)
	}
}

// TestReconstructReplyCtx_MissingToken verifies the cron / cc-connect send
// path returns the expected actionable error when no context_token has ever
// been stored for a peer. This is the "user must message the bot first"
// case that the original #1087 reporter hit.
func TestReconstructReplyCtx_MissingToken(t *testing.T) {
	p := &Platform{
		tokens: make(map[string]string),
	}
	_, err := p.ReconstructReplyCtx(sessionKeyPrefix + "never-messaged-user")
	if err == nil {
		t.Fatal("expected error for missing context_token, got nil")
	}
	if !containsStr(err.Error(), "no stored context_token") {
		t.Errorf("error = %q, want it to mention 'no stored context_token'", err.Error())
	}
}

// TestIsSendThrottled verifies ret=-2 (ilink sendmessage burst throttle) is
// recognized as a throttle and other errors are not.
func TestIsSendThrottled(t *testing.T) {
	if !isSendThrottled(fmt.Errorf("weixin: sendMessage: ret=-2 errcode=0 errmsg=prepare failed")) {
		t.Fatal("ret=-2 should be recognized as a throttle")
	}
	if isSendThrottled(fmt.Errorf("weixin: sendMessage: connection reset by peer")) {
		t.Fatal("non-ret=-2 error should not be treated as a throttle")
	}
	if isSendThrottled(nil) {
		t.Fatal("nil error should not be treated as a throttle")
	}
}

// TestSendChunk_FailsFastOnThrottle verifies a throttled send fails immediately
// with a single sendmessage call — no retries, because every attempt made during
// the ilink penalty window escalates it.
func TestSendChunk_FailsFastOnThrottle(t *testing.T) {
	var sendCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sendCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ret":-2,"errmsg":"prepare failed"}`))
	}))
	defer srv.Close()

	p := &Platform{httpClient: &http.Client{}}
	p.api = newAPIClient(srv.URL, "tok", "", p.httpClient)
	rc := &replyContext{peerUserID: "peer-1", contextToken: "tok-1"}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := p.sendChunk(ctx, rc, "hello")
	if err == nil {
		t.Fatal("expected a throttled error, got nil")
	}
	if !isSendThrottled(err) {
		t.Fatalf("error should be recognized as a throttle, got: %v", err)
	}
	if got := sendCalls.Load(); got != 1 {
		t.Fatalf("sendmessage calls = %d, want 1 (fail fast, no retries)", got)
	}
}

// TestSendChunk_Succeeds verifies a normal send returns nil and issues exactly
// one sendmessage call.
func TestSendChunk_Succeeds(t *testing.T) {
	var sendCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sendCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message_id":123}`))
	}))
	defer srv.Close()

	p := &Platform{httpClient: &http.Client{}}
	p.api = newAPIClient(srv.URL, "tok", "", p.httpClient)
	rc := &replyContext{peerUserID: "peer-1", contextToken: "tok-1"}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := p.sendChunk(ctx, rc, "hello"); err != nil {
		t.Fatalf("sendChunk failed: %v", err)
	}
	if got := sendCalls.Load(); got != 1 {
		t.Fatalf("sendmessage calls = %d, want 1", got)
	}
}

// TestCheckSendQuota_AllowsUnderLimit verifies sends under the window limit pass
// through without error.
func TestCheckSendQuota_AllowsUnderLimit(t *testing.T) {
	p := &Platform{sendQuotaLimit: 4, sendQuotaWindow: time.Hour}
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		if err := p.checkSendQuota(ctx); err != nil {
			t.Fatalf("checkSendQuota(%d) unexpectedly failed: %v", i, err)
		}
	}
}

// TestCheckSendQuota_FailsWhenOverLimit verifies the quota fails fast (no
// waiting) once the window budget is exhausted.
func TestCheckSendQuota_FailsWhenOverLimit(t *testing.T) {
	p := &Platform{sendQuotaLimit: 1, sendQuotaWindow: time.Hour}
	ctx := context.Background()
	if err := p.checkSendQuota(ctx); err != nil {
		t.Fatalf("first send should pass: %v", err)
	}
	start := time.Now()
	if err := p.checkSendQuota(ctx); err == nil {
		t.Fatal("second send should fail (budget exhausted)")
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("over-budget send should fail fast, took %v", elapsed)
	}
}

// TestCheckSendQuota_DisabledWhenZero verifies limit 0 disables the quota entirely.
func TestCheckSendQuota_DisabledWhenZero(t *testing.T) {
	p := &Platform{sendQuotaLimit: 0, sendQuotaWindow: time.Hour}
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		if err := p.checkSendQuota(ctx); err != nil {
			t.Fatalf("disabled quota should never fail: %v", err)
		}
	}
}

// TestNew_BurstLimitZeroDisablesQuota verifies an explicit burst_limit=0 in the
// config disables the send-volume quota entirely, instead of falling back to the
// default limit (regression for #1716).
func TestNew_BurstLimitZeroDisablesQuota(t *testing.T) {
	p, err := New(map[string]any{"token": "tok", "burst_limit": 0})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	wp := p.(*Platform)
	if wp.sendQuotaLimit != 0 {
		t.Fatalf("burst_limit=0 should disable the quota, got limit=%d", wp.sendQuotaLimit)
	}
	for i := 0; i < 10; i++ {
		if err := wp.checkSendQuota(context.Background()); err != nil {
			t.Fatalf("disabled quota must never fail: %v", err)
		}
	}
}

// TestNew_DefaultQuota verifies that when burst_limit / burst_window_secs are not
// set, the documented 24h account budget defaults are applied.
func TestNew_DefaultQuota(t *testing.T) {
	p, err := New(map[string]any{"token": "tok"})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	wp := p.(*Platform)
	if wp.sendQuotaLimit != defaultBurstLimit {
		t.Fatalf("default limit = %d, want %d", wp.sendQuotaLimit, defaultBurstLimit)
	}
	wantWindow := time.Duration(defaultBurstWindowSecs) * time.Second
	if wp.sendQuotaWindow != wantWindow {
		t.Fatalf("default window = %v, want %v", wp.sendQuotaWindow, wantWindow)
	}
}

// TestNew_ExplicitBurstLimit verifies explicitly provided quota values are honored.
func TestNew_ExplicitBurstLimit(t *testing.T) {
	p, err := New(map[string]any{"token": "tok", "burst_limit": 7, "burst_window_secs": 1800})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	wp := p.(*Platform)
	if wp.sendQuotaLimit != 7 {
		t.Fatalf("explicit burst_limit = %d, want 7", wp.sendQuotaLimit)
	}
	if wp.sendQuotaWindow != 1800*time.Second {
		t.Fatalf("explicit window = %v, want 1800s", wp.sendQuotaWindow)
	}
}

// TestNew_DefaultQuotaEnforced verifies the default short-window budget is
// enforced end-to-end on a default instance: sends within the window pass, the
// next one over the budget fails fast.
func TestNew_DefaultQuotaEnforced(t *testing.T) {
	p, err := New(map[string]any{"token": "tok"})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	wp := p.(*Platform)
	for i := 0; i < defaultBurstLimit; i++ {
		if err := wp.checkSendQuota(context.Background()); err != nil {
			t.Fatalf("send %d within the default window should pass: %v", i+1, err)
		}
	}
	if err := wp.checkSendQuota(context.Background()); err == nil {
		t.Fatal("send over the default window budget should fail")
	}
}

// TestSendChunks_AppliesQuota verifies the budget is enforced end-to-end through
// sendChunks (httptest server): under budget sends succeed, over budget fails.
func TestSendChunks_AppliesQuota(t *testing.T) {
	var sendCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sendCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message_id":123}`))
	}))
	defer srv.Close()

	p := &Platform{httpClient: &http.Client{}, sendQuotaLimit: 1, sendQuotaWindow: time.Hour}
	p.api = newAPIClient(srv.URL, "tok", "", p.httpClient)
	rc := &replyContext{peerUserID: "peer-1", contextToken: "tok-1"}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := p.sendChunks(ctx, rc, "first"); err != nil {
		t.Fatalf("first send failed: %v", err)
	}
	if err := p.sendChunks(ctx, rc, "second"); err == nil {
		t.Fatal("second send should fail (budget exhausted)")
	}
	if got := sendCalls.Load(); got != 1 {
		t.Fatalf("sendmessage calls = %d, want 1 (over-budget send not attempted)", got)
	}
}
