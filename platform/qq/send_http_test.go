package qq

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

// fakeOneBot is a mock OneBot v11 server (NapCat-style) exposing a forward
// WebSocket and an HTTP API on the same address, recording which transport
// each send action arrived on.
type fakeOneBot struct {
	ts *httptest.Server

	mu         sync.Mutex
	wsActions  []string
	httpPaths  []string
	httpBodies []map[string]any
}

func newFakeOneBot(t *testing.T) *fakeOneBot {
	t.Helper()
	f := &fakeOneBot{}
	upgrader := websocket.Upgrader{}
	f.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if websocket.IsWebSocketUpgrade(r) {
			c, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer func() { _ = c.Close() }()
			for {
				_, msg, err := c.ReadMessage()
				if err != nil {
					return
				}
				var req map[string]any
				if json.Unmarshal(msg, &req) != nil {
					continue
				}
				action, _ := req["action"].(string)
				if action != "get_login_info" {
					f.mu.Lock()
					f.wsActions = append(f.wsActions, action)
					f.mu.Unlock()
				}
				raw, _ := json.Marshal(map[string]any{
					"status": "ok", "retcode": 0, "echo": req["echo"],
					"data": map[string]any{"user_id": 999999, "message_id": 1},
				})
				_ = c.WriteMessage(websocket.TextMessage, raw)
			}
		}

		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.httpPaths = append(f.httpPaths, r.URL.Path)
		f.httpBodies = append(f.httpBodies, body)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok", "retcode": 0, "data": map[string]any{"message_id": 2},
		})
	}))
	t.Cleanup(f.ts.Close)
	return f
}

func (f *fakeOneBot) snapshot() (ws, httpPaths []string, bodies []map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.wsActions...), append([]string(nil), f.httpPaths...), append([]map[string]any(nil), f.httpBodies...)
}

func startFakePlatform(t *testing.T, f *fakeOneBot, opts map[string]any) *Platform {
	t.Helper()
	opts["ws_url"] = "ws" + strings.TrimPrefix(f.ts.URL, "http")
	p, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	pl := p.(*Platform)
	if err := pl.Start(func(core.Platform, *core.Message) {}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = pl.Stop() })
	return pl
}

func TestNew_SendViaHTTPDefaultsFalse(t *testing.T) {
	p, _ := New(map[string]any{"http_url": "http://127.0.0.1:3000"})
	if p.(*Platform).sendViaHTTP {
		t.Error("sendViaHTTP should default to false")
	}
}

// Default config (http_url set for file uploads, send_via_http unset): text
// sends must keep using the WebSocket, exactly as before.
func TestSend_DefaultUsesWebSocket(t *testing.T) {
	f := newFakeOneBot(t)
	p := startFakePlatform(t, f, map[string]any{"http_url": f.ts.URL})

	long := strings.Repeat("长", 3000)
	if err := p.Send(context.Background(), &replyContext{messageType: "group", groupID: 42}, long); err != nil {
		t.Fatalf("Send group: %v", err)
	}
	if err := p.Send(context.Background(), &replyContext{messageType: "private", userID: 7}, "hi"); err != nil {
		t.Fatalf("Send private: %v", err)
	}

	ws, httpPaths, _ := f.snapshot()
	if want := []string{"send_group_msg", "send_private_msg"}; strings.Join(ws, ",") != strings.Join(want, ",") {
		t.Errorf("ws actions = %v, want %v", ws, want)
	}
	if len(httpPaths) != 0 {
		t.Errorf("http calls = %v, want none", httpPaths)
	}
}

func TestSend_SendViaHTTPUsesHTTPAPI(t *testing.T) {
	f := newFakeOneBot(t)
	p := startFakePlatform(t, f, map[string]any{"http_url": f.ts.URL + "/", "send_via_http": true})

	long := strings.Repeat("长", 3000)
	if err := p.Send(context.Background(), &replyContext{messageType: "group", groupID: 42}, long); err != nil {
		t.Fatalf("Send group: %v", err)
	}
	if err := p.Send(context.Background(), &replyContext{messageType: "private", userID: 7}, "hi"); err != nil {
		t.Fatalf("Send private: %v", err)
	}

	ws, httpPaths, bodies := f.snapshot()
	if len(ws) != 0 {
		t.Errorf("ws actions = %v, want none", ws)
	}
	if want := []string{"/send_group_msg", "/send_private_msg"}; strings.Join(httpPaths, ",") != strings.Join(want, ",") {
		t.Fatalf("http paths = %v, want %v", httpPaths, want)
	}
	if bodies[0]["message"] != long || bodies[0]["group_id"] != float64(42) {
		t.Errorf("group body = %v, want message of %d bytes and group_id 42", bodies[0]["group_id"], len(long))
	}
	if bodies[1]["message"] != "hi" || bodies[1]["user_id"] != float64(7) {
		t.Errorf("private body = %v", bodies[1])
	}
}

// send_via_http without http_url has no HTTP endpoint to use: keep delivering
// over the WebSocket rather than failing every send.
func TestSend_SendViaHTTPWithoutHTTPURLFallsBackToWebSocket(t *testing.T) {
	f := newFakeOneBot(t)
	p := startFakePlatform(t, f, map[string]any{"send_via_http": true})

	if err := p.Send(context.Background(), &replyContext{messageType: "group", groupID: 42}, "hello"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	ws, httpPaths, _ := f.snapshot()
	if len(ws) != 1 || ws[0] != "send_group_msg" {
		t.Errorf("ws actions = %v, want [send_group_msg]", ws)
	}
	if len(httpPaths) != 0 {
		t.Errorf("http calls = %v, want none", httpPaths)
	}
}
