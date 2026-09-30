package wecom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

func quotaOptions(minute, hour int) map[string]any {
	return map[string]any{"enabled": true, "messages_per_minute": minute, "messages_per_hour": hour, "uploads_per_minute": 10, "uploads_per_hour": 100}
}

type httpQuotaRequest struct{ path, token, kind, recipient string }
type httpQuotaFixture struct {
	mu          sync.Mutex
	requests    []httpQuotaRequest
	failMessage bool
	server      *httptest.Server
}

func newHTTPQuotaFixture(t *testing.T) *httpQuotaFixture {
	t.Helper()
	f := &httpQuotaFixture{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MsgType string `json:"msgtype"`
			ToUser  string `json:"touser"`
		}
		if r.URL.Path == "/cgi-bin/message/send" {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
		}
		f.mu.Lock()
		f.requests = append(f.requests, httpQuotaRequest{r.URL.Path, r.URL.Query().Get("access_token"), body.MsgType, body.ToUser})
		fail := f.failMessage
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		var response string
		switch r.URL.Path {
		case "/cgi-bin/gettoken":
			response = `{"access_token":"fresh-token","expires_in":7200,"errcode":0}`
		case "/cgi-bin/media/upload":
			response = `{"media_id":"image-1","errcode":0}`
		case "/cgi-bin/message/send":
			if fail {
				response = `{"errcode":45009,"errmsg":"rate limited"}`
			} else {
				response = `{"errcode":0}`
			}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if _, err := fmt.Fprint(w, response); err != nil {
			t.Errorf("write quota fixture response: %v", err)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}
func (f *httpQuotaFixture) platform(t *testing.T, cfg map[string]any) *Platform {
	t.Helper()
	pf, err := New(map[string]any{"corp_id": t.Name(), "corp_secret": "test-secret", "agent_id": "1", "callback_token": "test-token", "callback_aes_key": strings.Repeat("A", 43), "api_base_url": f.server.URL, "outbound_quota": cfg})
	if err != nil {
		t.Fatal(err)
	}
	p := pf.(*Platform)
	t.Cleanup(func() {
		if err := p.Stop(); err != nil {
			t.Error(err)
		}
	})
	return p
}
func (f *httpQuotaFixture) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if r.path == path {
			n++
		}
	}
	return n
}
func shortQuotaContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	t.Cleanup(cancel)
	return ctx
}

func TestHTTPQuota_LongReplyCountsEachChunk(t *testing.T) {
	for _, markdown := range []bool{false, true} {
		t.Run(fmt.Sprint(markdown), func(t *testing.T) {
			f := newHTTPQuotaFixture(t)
			p := f.platform(t, quotaOptions(1, 100))
			p.enableMarkdown = markdown
			err := p.Reply(shortQuotaContext(t), replyContext{userID: "u1"}, strings.Repeat("x", 4001))
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("quota did not stop subsequent chunks: %v", err)
			}
			if got := f.count("/cgi-bin/message/send"); got != 1 {
				t.Fatalf("sent %d chunks, want 1", got)
			}
			if err := p.Send(context.Background(), replyContext{userID: "u2"}, "unrelated user"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestHTTPQuota_ImageAndTextShareRecipientBudget(t *testing.T) {
	f := newHTTPQuotaFixture(t)
	p := f.platform(t, quotaOptions(1, 100))
	image := core.ImageAttachment{Data: []byte("image"), FileName: "image.png"}
	if err := p.SendImage(context.Background(), replyContext{userID: "u1"}, image); err != nil {
		t.Fatal(err)
	}
	if err := p.Send(shortQuotaContext(t), replyContext{userID: "u1"}, "text"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if got := f.count("/cgi-bin/message/send"); got != 1 {
		t.Fatalf("sent %d messages", got)
	}
	if err := p.SendImage(context.Background(), replyContext{userID: "u2"}, image); err != nil {
		t.Fatal(err)
	}
	if got := f.count("/cgi-bin/media/upload"); got != 2 {
		t.Fatalf("uploads=%d", got)
	}
}

func TestHTTPQuota_IgnoresUploadLimitsAndSharesMessageBudget(t *testing.T) {
	f := newHTTPQuotaFixture(t)
	first, second := quotaOptions(1, 100), quotaOptions(1, 100)
	first["uploads_per_minute"], first["uploads_per_hour"] = 1, 1
	second["uploads_per_minute"], second["uploads_per_hour"] = 2, 2
	p1, p2 := f.platform(t, first), f.platform(t, second)
	image := core.ImageAttachment{Data: []byte("image"), FileName: "image.png"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// HTTP uploads have no bot-wide budget, even when uploads_* is configured.
	for i, p := range []*Platform{p1, p1, p2} {
		if err := p.SendImage(ctx, replyContext{userID: fmt.Sprintf("u%d", i)}, image); err != nil {
			t.Fatalf("HTTP upload %d was limited by uploads_*: %v", i+1, err)
		}
	}
	// Different ignored upload settings must not split the shared message budget.
	if err := p2.Send(shortQuotaContext(t), replyContext{userID: "u0"}, "text"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shared recipient quota bypassed: %v", err)
	}
	if got := f.count("/cgi-bin/media/upload"); got != 3 {
		t.Fatalf("uploads=%d, want 3", got)
	}
	if got := f.count("/cgi-bin/message/send"); got != 3 {
		t.Fatalf("messages=%d, want 3", got)
	}
}

func TestHTTPQuota_SharedFactoryIdentityAndFailedRequestCharged(t *testing.T) {
	f := newHTTPQuotaFixture(t)
	cfg := quotaOptions(1, 100)
	p1 := f.platform(t, cfg)
	p2 := f.platform(t, cfg)
	f.mu.Lock()
	f.failMessage = true
	f.mu.Unlock()
	if err := p1.Send(context.Background(), replyContext{userID: "u1"}, "first"); err == nil {
		t.Fatal("server error ignored")
	}
	if err := p2.Send(shortQuotaContext(t), replyContext{userID: "u1"}, "second"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shared account bypassed: %v", err)
	}
	if got := f.count("/cgi-bin/message/send"); got != 1 {
		t.Fatalf("failed request retried/refunded: %d", got)
	}
}

func TestHTTPQuota_StopCancelsBackgroundWait(t *testing.T) {
	f := newHTTPQuotaFixture(t)
	p := f.platform(t, quotaOptions(1, 100))
	if err := p.Send(context.Background(), replyContext{userID: "u"}, "first"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- p.Send(context.Background(), replyContext{userID: "u"}, "blocked") }()
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Stop did not cancel quota wait")
	}
	if got := f.count("/cgi-bin/message/send"); got != 1 {
		t.Fatalf("sent after stop: %d", got)
	}
}

type wsQuotaFixture struct {
	p          *WSPlatform
	mu         sync.Mutex
	frames     []wsFrame
	serverMu   sync.Mutex
	serverConn *websocket.Conn
}

func newWSQuotaFixture(t *testing.T, cfg map[string]any) *wsQuotaFixture {
	t.Helper()
	f := &wsQuotaFixture{}
	connected := make(chan struct{})
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() {
			if err := conn.Close(); err != nil {
				t.Errorf("close quota fixture websocket: %v", err)
			}
		}()
		f.serverConn = conn
		close(connected)
		for {
			var frame wsFrame
			if err := conn.ReadJSON(&frame); err != nil {
				return
			}
			f.mu.Lock()
			f.frames = append(f.frames, frame)
			f.mu.Unlock()
			body := map[string]string{}
			if frame.Cmd == "aibot_upload_media_init" {
				body["upload_id"] = "upload-1"
			}
			if frame.Cmd == "aibot_upload_media_finish" {
				body["media_id"] = "media-1"
			}
			f.serverMu.Lock()
			err := conn.WriteJSON(map[string]any{"headers": frame.Headers, "errcode": 0, "body": body})
			f.serverMu.Unlock()
			if err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	pf, err := New(map[string]any{"mode": "websocket", "bot_id": t.Name(), "bot_secret": "test-secret", "ws_url": wsURL, "allow_from": "allowed-user", "outbound_quota": cfg})
	if err != nil {
		t.Fatal(err)
	}
	p := pf.(*WSPlatform)
	f.p = p
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	<-connected
	p.conn = conn
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			var frame wsFrame
			if err := conn.ReadJSON(&frame); err != nil {
				return
			}
			p.handleFrame(frame)
		}
	}()
	t.Cleanup(func() {
		if err := p.Stop(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Error(err)
		}
		<-readDone
	})
	return f
}
func (f *wsQuotaFixture) count(cmd string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, frame := range f.frames {
		if frame.Cmd == cmd {
			n++
		}
	}
	return n
}
func (f *wsQuotaFixture) inject(t *testing.T, frame any) {
	t.Helper()
	f.serverMu.Lock()
	defer f.serverMu.Unlock()
	if err := f.serverConn.WriteJSON(frame); err != nil {
		t.Fatal(err)
	}
}
func wsQuotaContext(chat, user string) wsReplyContext {
	return wsReplyContext{reqID: "callback-" + chat + "-" + user, chatID: chat, userID: user}
}

func TestWSQuota_MediaCannotBypassMessageBudget(t *testing.T) {
	for _, kind := range []string{"image", "file"} {
		t.Run(kind, func(t *testing.T) {
			f := newWSQuotaFixture(t, quotaOptions(1, 100))
			p := f.p
			if err := p.Send(context.Background(), wsQuotaContext("group", "u1"), "first"); err != nil {
				t.Fatal(err)
			}
			send := func(ctx context.Context, chat string) error {
				if kind == "image" {
					return p.SendImage(ctx, wsQuotaContext(chat, "u2"), core.ImageAttachment{Data: []byte("png"), FileName: "a.png"})
				}
				return p.SendFile(ctx, wsQuotaContext(chat, "u2"), core.FileAttachment{Data: []byte("file"), FileName: "a.txt"})
			}
			if err := send(shortQuotaContext(t), "group"); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("media bypassed message quota: %v", err)
			}
			if got := f.count("aibot_upload_media_init"); got != 1 {
				t.Fatalf("uploads=%d", got)
			}
			if got := f.count("aibot_send_msg"); got != 1 {
				t.Fatalf("messages=%d", got)
			}
			if err := send(context.Background(), "other"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWSQuota_ReplyAndSendShareGroupQuota(t *testing.T) {
	f := newWSQuotaFixture(t, quotaOptions(1, 100))
	p := f.p
	if err := p.Reply(context.Background(), wsQuotaContext("group", "u1"), "reply"); err != nil {
		t.Fatal(err)
	}
	if err := p.Send(shortQuotaContext(t), wsQuotaContext("group", "u2"), "same group"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("group quota split by user: %v", err)
	}
	if err := p.Send(context.Background(), wsQuotaContext("other", "u2"), "other group"); err != nil {
		t.Fatal(err)
	}
	if got := f.count("aibot_respond_msg"); got != 1 {
		t.Fatalf("replies=%d", got)
	}
}

func TestWSQuota_LongReplySendCountsEachChunk(t *testing.T) {
	f := newWSQuotaFixture(t, quotaOptions(1, 100))
	if err := f.p.Send(shortQuotaContext(t), wsQuotaContext("group", "u"), strings.Repeat("x", 4001)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if got := f.count("aibot_send_msg"); got != 1 {
		t.Fatalf("chunks=%d", got)
	}
}

func TestWSQuota_UploadBudgetIsBotWideAndSeparateFromMessages(t *testing.T) {
	cfg := quotaOptions(2, 100)
	cfg["uploads_per_minute"] = 1
	f := newWSQuotaFixture(t, cfg)
	p := f.p
	file := core.FileAttachment{Data: []byte(strings.Repeat("x", wecomWSUploadChunkSize+1)), FileName: "a.txt"}
	if err := p.SendFile(context.Background(), wsQuotaContext("a", "u"), file); err != nil {
		t.Fatal(err)
	}
	if got := f.count("aibot_upload_media_chunk"); got != 2 {
		t.Fatalf("chunks=%d", got)
	}
	if err := p.SendImage(shortQuotaContext(t), wsQuotaContext("b", "u"), core.ImageAttachment{Data: []byte("png")}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("upload quota bypassed across chats: %v", err)
	}
	if got := f.count("aibot_upload_media_init"); got != 1 {
		t.Fatalf("uploads=%d", got)
	}
	if err := p.Send(context.Background(), wsQuotaContext("a", "u"), "text still fits"); err != nil {
		t.Fatal(err)
	}
}

func TestWSQuota_UnauthorizedWaitDoesNotBlockAcksOrHeartbeat(t *testing.T) {
	f := newWSQuotaFixture(t, quotaOptions(1, 100))
	p := f.p
	received := make(chan *core.Message, 1)
	p.handler = func(_ core.Platform, msg *core.Message) { received <- msg }
	if err := p.Send(context.Background(), wsQuotaContext("blocked", "u"), "fills quota"); err != nil {
		t.Fatal(err)
	}
	for i := range 500 {
		f.inject(t, unauthorizedCallback(t, i, "blocked"))
	}
	p.missedPong.Store(2)
	f.inject(t, map[string]any{"headers": map[string]string{"req_id": "ping_quota"}, "errcode": 0})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.writeAndWaitAckStrict(ctx, map[string]any{"cmd": "ping", "headers": map[string]string{"req_id": "control-quota"}}, "control-quota", time.Second); err != nil {
		t.Fatalf("read loop stalled behind unauthorized quota wait: %v", err)
	}
	if p.missedPong.Load() != 0 {
		t.Fatal("heartbeat ACK was not processed")
	}
	allowed := wsMsgCallbackBody{MsgID: "allowed-after-flood", ChatID: "allowed-chat", MsgType: "text"}
	allowed.From.UserID = "allowed-user"
	allowed.Text.Content = "authorized message"
	f.inject(t, wsCallbackFrame(t, "callback-allowed", allowed))
	select {
	case msg := <-received:
		if msg.UserID != "allowed-user" || msg.Content != "authorized message" {
			t.Fatalf("wrong message reached handler: %+v", msg)
		}
	case <-ctx.Done():
		t.Fatal("authorized message was blocked by denial flood")
	}
	if err := p.Send(ctx, wsQuotaContext("allowed-chat", "allowed-user"), "authorized response"); err != nil {
		t.Fatalf("authorized response/ACK blocked by denial flood: %v", err)
	}
	if got := f.count("aibot_respond_msg"); got != 0 {
		t.Fatalf("unauthorized response bypassed quota: %d", got)
	}
}

func TestWSQuota_UnauthorizedSuccessReleasesSlot(t *testing.T) {
	f := newWSQuotaFixture(t, quotaOptions(10, 100))
	for i := range 2 {
		f.p.handleFrame(unauthorizedCallback(t, i, "chat"))
		waitUnauthorizedIdle(t, f.p)
		// The ACK barrier ensures the server observed the preceding reply frame.
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := f.p.Send(ctx, wsQuotaContext("other-chat", "allowed-user"), "barrier")
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if got := f.count("aibot_respond_msg"); got != i+1 {
			t.Fatalf("successful denial did not release slot: replies=%d", got)
		}
	}
}

func TestWSQuota_DefaultDisabled(t *testing.T) {
	f := newWSQuotaFixture(t, map[string]any{})
	for range 25 {
		if err := f.p.Send(context.Background(), wsQuotaContext("chat", "u"), "default unlimited"); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.count("aibot_send_msg"); got != 25 {
		t.Fatalf("sent=%d", got)
	}
}

func TestWSQuota_StopIsIdempotentAndCancelsWait(t *testing.T) {
	f := newWSQuotaFixture(t, quotaOptions(1, 100))
	if err := f.p.Send(context.Background(), wsQuotaContext("chat", "u"), "first"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- f.p.Send(context.Background(), wsQuotaContext("chat", "u"), "blocked") }()
	if err := f.p.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := f.p.Stop(); err != nil {
		t.Fatalf("repeated Stop: %v", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("background waiter survived Stop")
	}
	if got := f.count("aibot_send_msg"); got != 1 {
		t.Fatalf("sent after stop: %d", got)
	}
}
