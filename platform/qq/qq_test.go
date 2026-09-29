package qq

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"

	"github.com/gorilla/websocket"
)

func TestPlatform_Name(t *testing.T) {
	p := &Platform{}
	if got := p.Name(); got != "qq" {
		t.Errorf("Name() = %q, want %q", got, "qq")
	}
}

func TestNew_DefaultWSURL(t *testing.T) {
	p, err := New(map[string]any{})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	platform := p.(*Platform)
	if platform.wsURL != "ws://127.0.0.1:3001" {
		t.Errorf("wsURL = %q, want %q", platform.wsURL, "ws://127.0.0.1:3001")
	}
}

func TestNew_CustomWSURL(t *testing.T) {
	p, err := New(map[string]any{
		"ws_url": "ws://example.com:8080",
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	platform := p.(*Platform)
	if platform.wsURL != "ws://example.com:8080" {
		t.Errorf("wsURL = %q, want %q", platform.wsURL, "ws://example.com:8080")
	}
}

func TestNew_WithToken(t *testing.T) {
	p, err := New(map[string]any{
		"token": "my-secret-token",
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	platform := p.(*Platform)
	if platform.token != "my-secret-token" {
		t.Errorf("token = %q, want %q", platform.token, "my-secret-token")
	}
}

func TestNew_WithAllowFrom(t *testing.T) {
	p, err := New(map[string]any{
		"allow_from": "user1,user2,*",
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	platform := p.(*Platform)
	if platform.allowFrom != "user1,user2,*" {
		t.Errorf("allowFrom = %q, want %q", platform.allowFrom, "user1,user2,*")
	}
}

func TestNew_ShareSessionInChannel(t *testing.T) {
	p, err := New(map[string]any{
		"share_session_in_channel": true,
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	platform := p.(*Platform)
	if !platform.shareSessionInChannel {
		t.Error("shareSessionInChannel = false, want true")
	}
}

// verify Platform implements core.Platform
var _ core.Platform = (*Platform)(nil)

// TestStart_FetchesSelfIDWithoutTimeout verifies that Start() completes
// promptly with selfID populated from the get_login_info OneBot API call.
// Regression for a bug where Start invoked callAPI BEFORE launching readLoop,
// so the API response had no consumer and callAPI always timed out after 15s
// — leaving selfID=0 and disabling the self-message filter in handleMessage.
func TestStart_FetchesSelfIDWithoutTimeout(t *testing.T) {
	const botUserID = 999999

	upgrader := websocket.Upgrader{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			_, msg, err := c.ReadMessage()
			if err != nil {
				return
			}
			var req map[string]any
			if err := json.Unmarshal(msg, &req); err != nil {
				continue
			}
			if req["action"] == "get_login_info" {
				echo, _ := req["echo"].(string)
				resp := map[string]any{
					"status":  "ok",
					"retcode": 0,
					"echo":    echo,
					"data":    map[string]any{"user_id": botUserID, "nickname": "TestBot"},
				}
				raw, _ := json.Marshal(resp)
				_ = c.WriteMessage(websocket.TextMessage, raw)
			}
		}
	}))
	defer ts.Close()

	p := &Platform{
		wsURL: "ws" + strings.TrimPrefix(ts.URL, "http"),
	}

	done := make(chan error, 1)
	go func() {
		done <- p.Start(func(core.Platform, *core.Message) {})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		_ = p.Stop()
		t.Fatal("Start did not complete within 5s; readLoop likely starts after callAPI, so get_login_info never gets a response")
	}
	defer p.Stop()

	if got := p.selfID.Load(); got != botUserID {
		t.Errorf("selfID = %d, want %d (self-message filter would be disabled)", got, botUserID)
	}
}

// fakeOneBot is a OneBot WebSocket server for tests. It answers every action
// with an ok response echoing the request, except the actions listed in
// silent, and lets the test push message events to the connected client.
type fakeOneBot struct {
	t         *testing.T
	srv       *httptest.Server
	silent    map[string]bool
	connected chan struct{}

	mu   sync.Mutex // serializes writes to conn
	conn *websocket.Conn
}

func newFakeOneBot(t *testing.T, silent ...string) *fakeOneBot {
	t.Helper()
	f := &fakeOneBot{t: t, silent: map[string]bool{}, connected: make(chan struct{})}
	for _, a := range silent {
		f.silent[a] = true
	}
	upgrader := websocket.Upgrader{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		f.mu.Lock()
		f.conn = c
		f.mu.Unlock()
		close(f.connected)
		for {
			_, msg, err := c.ReadMessage()
			if err != nil {
				return
			}
			var req map[string]any
			if err := json.Unmarshal(msg, &req); err != nil {
				continue
			}
			action, _ := req["action"].(string)
			if f.silent[action] {
				continue
			}
			resp := map[string]any{"status": "ok", "retcode": 0, "echo": req["echo"], "data": map[string]any{}}
			if action == "get_login_info" {
				resp["data"] = map[string]any{"user_id": 999999, "nickname": "TestBot"}
			}
			f.write(resp)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOneBot) url() string { return "ws" + strings.TrimPrefix(f.srv.URL, "http") }

func (f *fakeOneBot) write(v any) {
	raw, _ := json.Marshal(v)
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.conn.WriteMessage(websocket.TextMessage, raw); err != nil {
		f.t.Errorf("fake OneBot write: %v", err)
	}
}

// sendMessages pushes private message events with message_id from..to.
func (f *fakeOneBot) sendMessages(from, to int) {
	for id := from; id <= to; id++ {
		f.write(map[string]any{
			"post_type":    "message",
			"message_type": "private",
			"message_id":   id,
			"user_id":      123,
			"self_id":      999999,
			"message":      []any{map[string]any{"type": "text", "data": map[string]any{"text": "hi"}}},
			"sender":       map[string]any{"nickname": "u"},
		})
	}
}

func startPlatform(t *testing.T, f *fakeOneBot, handler core.MessageHandler) *Platform {
	t.Helper()
	p := &Platform{wsURL: f.url(), allowFrom: "*"}
	if err := p.Start(handler); err != nil {
		t.Fatal(err)
	}
	<-f.connected
	return p
}

// Regression for a deadlock where readLoop called the message handler
// directly: a handler that replied through callAPI waited for a response that
// only readLoop could deliver, so every reply sent while handling a message
// timed out after 15s.
func TestHandler_CanCallAPI(t *testing.T) {
	f := newFakeOneBot(t)
	replied := make(chan error, 1)
	var p *Platform
	p = startPlatform(t, f, func(core.Platform, *core.Message) {
		_, err := p.callAPI(context.Background(), "send_private_msg", map[string]any{"user_id": 123, "message": "ok"})
		replied <- err
	})
	defer func() { _ = p.Stop() }()
	f.sendMessages(1, 1)

	select {
	case err := <-replied:
		if err != nil {
			t.Fatalf("callAPI from handler: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("callAPI from the message handler did not return within 5s; the handler is blocking readLoop")
	}
}

// messageRecorder is a handler that records message IDs in handling order and
// blocks on gate while handling the first message.
type messageRecorder struct {
	entered chan struct{} // closed when the first message is being handled
	gate    chan struct{} // close to let the first message finish
	mu      sync.Mutex
	ids     []string
	once    sync.Once
}

func newMessageRecorder() *messageRecorder {
	return &messageRecorder{entered: make(chan struct{}), gate: make(chan struct{})}
}

func (r *messageRecorder) handle(_ core.Platform, msg *core.Message) {
	first := false
	r.once.Do(func() { first = true })
	if first {
		close(r.entered)
		<-r.gate
	}
	r.mu.Lock()
	r.ids = append(r.ids, msg.MessageID)
	r.mu.Unlock()
}

func (r *messageRecorder) handled() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ids...)
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// With the handler stuck on one message, a burst larger than the event queue
// must not block readLoop: API responses that arrive after the burst still
// reach callAPI, the overflow is dropped and counted, and the queued messages
// are handled in arrival order once the handler resumes.
func TestReadLoop_QueueOverflowKeepsAPIResponsesFlowing(t *testing.T) {
	f := newFakeOneBot(t)
	rec := newMessageRecorder()
	p := startPlatform(t, f, rec.handle)
	defer func() { _ = p.Stop() }()

	f.sendMessages(1, 1)
	<-rec.entered
	// Message 1 is in the handler; 2..65 fill the queue; 66 overflows.
	last := eventQueueSize + 2
	f.sendMessages(2, last)

	// The response to this call is written after the burst, so it only
	// arrives if readLoop got past all of it.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := p.callAPI(ctx, "get_status", nil); err != nil {
		t.Fatalf("callAPI behind a full event queue: %v", err)
	}
	if got := p.DroppedEvents(); got != 1 {
		t.Errorf("DroppedEvents() = %d, want 1", got)
	}

	close(rec.gate)
	waitFor(t, 2*time.Second, func() bool { return len(rec.handled()) == last-1 }, "queued messages to be handled")
	for i, id := range rec.handled() {
		if want := strconv.Itoa(i + 1); id != want {
			t.Fatalf("handled[%d] = %s, want %s (not FIFO); handled = %v", i, id, want, rec.handled())
		}
	}
}

// Stop must cancel an API call the handler is waiting on, so the handler
// returns and Stop can join readLoop and handleLoop.
func TestStop_CancelsAPICallInHandler(t *testing.T) {
	f := newFakeOneBot(t, "no_reply")
	waiting := make(chan struct{})
	result := make(chan error, 1)
	var p *Platform
	p = startPlatform(t, f, func(core.Platform, *core.Message) {
		close(waiting)
		_, err := p.callAPI(context.Background(), "no_reply", nil)
		result <- err
	})
	f.sendMessages(1, 1)
	<-waiting
	time.Sleep(50 * time.Millisecond) // let callAPI start waiting

	start := time.Now()
	if err := p.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Stop took %s, want it to return promptly", d)
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("callAPI error = %v, want context.Canceled", err)
		}
	default:
		t.Fatal("handler still waiting after Stop returned")
	}
	if err := p.Stop(); err != nil {
		t.Errorf("second Stop: %v", err)
	}
}

// A handler that ignores cancellation must not make Stop hang.
func TestStop_ReturnsWhenHandlerIgnoresCancellation(t *testing.T) {
	f := newFakeOneBot(t)
	rec := newMessageRecorder()
	defer close(rec.gate)
	p := startPlatform(t, f, rec.handle)
	f.sendMessages(1, 1)
	<-rec.entered

	start := time.Now()
	if err := p.Stop(); err == nil {
		t.Error("Stop returned nil while the handler was still running")
	}
	if d := time.Since(start); d > stopTimeout+2*time.Second {
		t.Errorf("Stop took %s, want about %s", d, stopTimeout)
	}
}

// One platform instance with a stuck handler and a full queue must not affect
// another instance.
func TestPlatforms_Isolated(t *testing.T) {
	fa := newFakeOneBot(t)
	recA := newMessageRecorder()
	a := startPlatform(t, fa, recA.handle)
	defer func() {
		close(recA.gate)
		_ = a.Stop()
	}()

	fb := newFakeOneBot(t)
	var mu sync.Mutex
	var handledB []string
	b := startPlatform(t, fb, func(_ core.Platform, msg *core.Message) {
		mu.Lock()
		handledB = append(handledB, msg.MessageID)
		mu.Unlock()
	})
	defer func() { _ = b.Stop() }()

	fa.sendMessages(1, 1)
	<-recA.entered
	fa.sendMessages(2, eventQueueSize+11)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := a.callAPI(ctx, "get_status", nil); err != nil {
		t.Fatalf("A callAPI: %v", err)
	}
	if got := a.DroppedEvents(); got != 10 {
		t.Errorf("A DroppedEvents() = %d, want 10", got)
	}

	fb.sendMessages(1, 3)
	waitFor(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(handledB) == 3
	}, "B to handle its messages")
	if _, err := b.callAPI(ctx, "get_status", nil); err != nil {
		t.Fatalf("B callAPI: %v", err)
	}
	if got := b.DroppedEvents(); got != 0 {
		t.Errorf("B DroppedEvents() = %d, want 0", got)
	}
}
