package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

// wsRecoveryGateway keeps reading (and TCP-acknowledging) client Pings even
// when silent is set. This reproduces a live socket with a dead inbound path,
// rather than the ordinary disconnect that the old SDK already recovered.
type wsRecoveryGateway struct {
	server      *httptest.Server
	silent      atomic.Bool
	connections atomic.Int32
	active      atomic.Int32
	pongs       chan struct{}
	closed      chan struct{}
	replies     chan string
	mu          sync.Mutex
	sockets     []*websocket.Conn
}

func newWSRecoveryGateway(t *testing.T) *wsRecoveryGateway {
	t.Helper()
	g := &wsRecoveryGateway{pongs: make(chan struct{}, 32), closed: make(chan struct{}, 32), replies: make(chan string, 32)}
	g.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/open-apis/") {
			w.Header().Set("Content-Type", "application/json")
			switch {
			case strings.Contains(r.URL.Path, "tenant_access_token"):
				_, _ = io.WriteString(w, `{"code":0,"tenant_access_token":"test-token","expire":7200}`)
			case r.URL.Path == "/open-apis/bot/v3/info":
				_, _ = io.WriteString(w, `{"code":0,"bot":{"open_id":"test-bot","app_name":"test"}}`)
			case strings.HasSuffix(r.URL.Path, "/reply"):
				g.replies <- r.URL.Path
				_, _ = io.WriteString(w, `{"code":0,"data":{"message_id":"test-reply"}}`)
			default:
				_, _ = io.WriteString(w, `{"code":0,"data":{"user":{"name":"Test","open_id":"test-user"}}}`)
			}
			return
		}
		if r.URL.Path == larkws.GenEndpointUri {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(larkws.EndpointResp{Data: &larkws.Endpoint{
				Url: "ws" + strings.TrimPrefix(g.server.URL, "http") + "/ws?service_id=1&device_id=test",
				ClientConfig: &larkws.ClientConfig{
					PingInterval: 1, ReconnectCount: 3, ReconnectInterval: 1,
				},
			}})
			return
		}
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		g.mu.Lock()
		g.sockets = append(g.sockets, conn)
		g.mu.Unlock()
		n := g.connections.Add(1)
		g.active.Add(1)
		defer func() {
			_ = conn.Close()
			g.active.Add(-1)
			g.closed <- struct{}{}
		}()
		// Each connection delivers an event through the real SDK dispatcher.
		payload := []byte(fmt.Sprintf(`{"schema":"2.0","header":{"event_id":"event-%d","event_type":"im.message.receive_v1"},"event":{"sender":{"sender_id":{"open_id":"test-user"},"sender_type":"user"},"message":{"message_id":"message-%d","chat_id":"test-chat","chat_type":"p2p","message_type":"text","content":"{\"text\":\"/version\"}","create_time":"%d"}}}`, n, n, time.Now().UnixMilli()))
		frame := &larkws.Frame{
			Method: int32(larkws.FrameTypeData),
			Headers: []larkws.Header{
				{Key: larkws.HeaderType, Value: string(larkws.MessageTypeEvent)},
				{Key: larkws.HeaderMessageID, Value: fmt.Sprintf("frame-%d", n)},
				{Key: larkws.HeaderSum, Value: "1"},
				{Key: larkws.HeaderSeq, Value: "0"},
			},
			Payload: payload,
		}
		data, err := frame.Marshal()
		if err != nil || conn.WriteMessage(websocket.BinaryMessage, data) != nil {
			return
		}
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var frame larkws.Frame
			if frame.Unmarshal(data) != nil || larkws.Headers(frame.Headers).GetString(larkws.HeaderType) != string(larkws.MessageTypePing) {
				continue
			}
			if g.silent.Load() {
				continue
			}
			frame.Headers = []larkws.Header{{Key: larkws.HeaderType, Value: string(larkws.MessageTypePong)}}
			data, err = frame.Marshal()
			if err != nil || conn.WriteMessage(websocket.BinaryMessage, data) != nil {
				return
			}
			select {
			case g.pongs <- struct{}{}:
			default:
			}
		}
	}))
	t.Cleanup(func() {
		g.server.Close()
		g.mu.Lock()
		defer g.mu.Unlock()
		for _, conn := range g.sockets {
			_ = conn.Close()
		}
	})
	return g
}

func startWSRecoveryPlatform(t *testing.T, g *wsRecoveryGateway) (*Platform, <-chan string) {
	t.Helper()
	events := make(chan string, 16)
	p := startWSRecoveryPlatformWithHandler(t, g, func(_ context.Context, event *larkim.P2MessageReceiveV1) error {
		events <- *event.Event.Message.MessageId
		return nil
	})
	waitWSRecoveryEvent(t, events, "message-1")
	return p, events
}

func startWSRecoveryPlatformWithHandler(t *testing.T, g *wsRecoveryGateway, handler func(context.Context, *larkim.P2MessageReceiveV1) error) *Platform {
	t.Helper()
	p := &Platform{
		appID: "test-app", appSecret: "test-secret", domain: g.server.URL,
		platformName: "feishu", isWSPrimary: true,
		eventHandler: dispatcher.NewEventDispatcher("", "").OnP2MessageReceiveV1(handler),
	}
	if err := p.startWebSocketMode(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Stop() })
	return p
}

func waitWSRecoveryEvent(t *testing.T, events <-chan string, want string) {
	t.Helper()
	select {
	case got := <-events:
		if got != want {
			t.Fatalf("event = %q, want %q", got, want)
		}
	case <-time.After(12 * time.Second):
		t.Fatalf("no %s event: connection did not recover and dispatch", want)
	}
}

func TestWebSocketRecoversSilentConnection(t *testing.T) {
	t.Parallel()
	g := newWSRecoveryGateway(t)
	g.silent.Store(true)
	_, events := startWSRecoveryPlatform(t, g)
	waitWSRecoveryEvent(t, events, "message-2")
	select {
	case <-g.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("recovery left the old WebSocket open")
	}
	if got := g.active.Load(); got != 1 {
		t.Fatalf("active connections = %d, want exactly one after recovery", got)
	}
}

func TestWebSocketHealthyIdleConnection(t *testing.T) {
	t.Parallel()
	g := newWSRecoveryGateway(t)
	_, events := startWSRecoveryPlatform(t, g)
	// The SDK allows 2 * the advertised 1s Ping interval + 5s grace.
	// Stay idle beyond that window; only Pongs keep this connection alive.
	deadline := time.NewTimer(9 * time.Second)
	defer deadline.Stop()
	pongs := 0
	for {
		select {
		case event := <-events:
			t.Fatalf("healthy idle connection unexpectedly reconnected: %s", event)
		case <-g.pongs:
			pongs++
		case <-deadline.C:
			if pongs < 2 || g.connections.Load() != 1 {
				t.Fatalf("pongs = %d, connections = %d, want continuing heartbeats on one connection", pongs, g.connections.Load())
			}
			return
		}
	}
}

func TestWebSocketStopClosesConnection(t *testing.T) {
	t.Parallel()
	g := newWSRecoveryGateway(t)
	p, _ := startWSRecoveryPlatform(t, g)
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-g.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop left the WebSocket open")
	}
	if got := g.active.Load(); got != 0 {
		t.Fatalf("active connections after Stop = %d, want zero", got)
	}
}

func TestWebSocketReconnectPreservesInFlightHandler(t *testing.T) {
	t.Parallel()
	g := newWSRecoveryGateway(t)
	g.silent.Store(true)
	started := make(chan context.Context, 1)
	release := make(chan struct{})
	defer close(release)
	events := make(chan string, 16)
	startWSRecoveryPlatformWithHandler(t, g, func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
		if *event.Event.Message.MessageId == "message-1" {
			started <- ctx
			<-release
		} else {
			events <- *event.Event.Message.MessageId
		}
		return nil
	})
	var firstContext context.Context
	select {
	case firstContext = <-started:
	case <-time.After(12 * time.Second):
		t.Fatal("first event handler did not start")
	}
	waitWSRecoveryEvent(t, events, "message-2")
	if err := firstContext.Err(); err != nil {
		t.Fatalf("reconnect cancelled the in-flight handler: %v", err)
	}
}

// The binary recovery test must preserve a running caller's control socket.
func TestWebSocketBinaryPreservesExistingHomeSocket(t *testing.T) {
	if os.Getenv("CC_CONNECT_TEST_BINARY") == "" {
		t.Skip("set CC_CONNECT_TEST_BINARY to an orbenv cc-connect binary")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".cc-connect", "run", "api.sock")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("recovery", TestWebSocketBinaryRecoversSilentConnection)
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("binary recovery test changed the caller's socket: %v", err)
	}
}

// Opt in with a locally built cc-connect to exercise the complete binary:
// configuration, platform startup, inbound events, engine commands and replies.
func TestWebSocketBinaryRecoversSilentConnection(t *testing.T) {
	binary := os.Getenv("CC_CONNECT_TEST_BINARY")
	if binary == "" {
		t.Skip("set CC_CONNECT_TEST_BINARY to a locally built cc-connect binary")
	}
	g := newWSRecoveryGateway(t)
	g.silent.Store(true)
	dir := t.TempDir()
	// CC_DATA_DIR is a client-side socket override, not a server setting.
	// Isolate both HOME and the configured data directory from the caller.
	home := t.TempDir()
	dataDir := filepath.Join(home, ".cc-connect")
	config := fmt.Sprintf(`data_dir = %q
[[projects]]
name = "ws-recovery"
[projects.agent]
type = "codex"
[projects.agent.options]
work_dir = %q
[[projects.platforms]]
type = "feishu"
[projects.platforms.options]
app_id = "test-app"
app_secret = "test-secret"
domain = %q
`, dataDir, dir, g.server.URL)
	configPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "cc-connect.log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	cmd := exec.Command(binary, "--config", configPath)
	cmd.Env = append(os.Environ(), "HOME="+home, "CC_DATA_DIR="+dataDir)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		if t.Failed() {
			data, _ := os.ReadFile(logPath)
			t.Logf("cc-connect log:\n%s", data)
		}
	})
	for _, id := range []string{"message-1", "message-2"} {
		select {
		case path := <-g.replies:
			if want := "/open-apis/im/v1/messages/" + id + "/reply"; path != want {
				t.Fatalf("reply path = %s, want %s", path, want)
			}
		case <-time.After(15 * time.Second):
			t.Fatalf("binary did not reply to %s", id)
		}
	}
	if g.connections.Load() != 2 {
		t.Fatalf("connections = %d, want one replacement connection", g.connections.Load())
	}
}
