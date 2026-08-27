package runtimeclient

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/chenhg5/cc-connect/runtimeprotocol"
	"github.com/gorilla/websocket"
)

type connectionTestCatalog struct{}

func (connectionTestCatalog) ListWorkspaces(context.Context) ([]core.Workspace, error) {
	return []core.Workspace{}, nil
}

func (connectionTestCatalog) ResolveWorkspace(context.Context, string) (core.Workspace, error) {
	return core.Workspace{}, nil
}

type connectionTestCheckpoint struct{}

func (connectionTestCheckpoint) RecordUnconfirmed(uint64, uint64, runtimeprotocol.Method, runtimeprotocol.Resource, []byte) error {
	return nil
}

func (connectionTestCheckpoint) Confirm(uint64, uint64) error { return nil }

type connectionTestServer struct {
	server    *httptest.Server
	connected chan struct{}
	ping      chan struct{}
}

func newConnectionTestServer(t *testing.T) *connectionTestServer {
	t.Helper()
	fixture := &connectionTestServer{
		connected: make(chan struct{}, 1),
		ping:      make(chan struct{}, 1),
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"data": map[string]string{
					"challenge":     "challenge",
					"contract_hash": runtimeprotocol.ContractHash,
				},
			})
			return
		}
		header := http.Header{"X-CC-Connection-Generation": []string{"1"}}
		connection, err := upgrader.Upgrade(w, r, header)
		if err != nil {
			t.Errorf("升级测试 WebSocket: %v", err)
			return
		}
		defer func() { _ = connection.Close() }()
		connection.SetPingHandler(func(payload string) error {
			select {
			case fixture.ping <- struct{}{}:
			default:
			}
			return connection.WriteControl(websocket.PongMessage, []byte(payload), time.Now().Add(time.Second))
		})
		fixture.connected <- struct{}{}
		for {
			if _, _, err := connection.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(func() {
		fixture.server.Close()
	})
	return fixture
}

func newConnectionTestClient(t *testing.T, serverURL string) *Client {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成测试密钥: %v", err)
	}
	handler := &Handler{
		dependencies:  Dependencies{Catalog: connectionTestCatalog{}},
		subscriptions: make(map[string]*runtimeSubscription),
		turnArtifacts: make(map[string][]string),
		terminalTurns: make(map[string]struct{}),
	}
	client, err := NewClient(ClientConfig{
		ServerURL: serverURL, DeviceID: "device-1", PrivateKey: privateKey,
		Handler: handler, Checkpoint: connectionTestCheckpoint{}, AllowInsecureLoopback: true,
	})
	if err != nil {
		t.Fatalf("创建测试客户端: %v", err)
	}
	client.pingEvery = 10 * time.Millisecond
	client.pongWait = 100 * time.Millisecond
	return client
}

func TestClientRunConnection_KeepsIdleWebSocketAlive(t *testing.T) {
	fixture := newConnectionTestServer(t)
	client := newConnectionTestClient(t, fixture.server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- client.runConnection(ctx) }()

	select {
	case <-fixture.connected:
	case <-time.After(time.Second):
		t.Fatal("Runtime 未连接测试控制面")
	}
	select {
	case <-fixture.ping:
	case <-time.After(time.Second):
		t.Fatal("空闲 Runtime 连接没有发送保活 ping")
	}
	if client.sequence != 0 {
		t.Fatalf("保活 ping 改变了应用事件序列: %d", client.sequence)
	}
	cancel()
	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("取消后 Runtime 连接未及时退出")
	}
}

func TestClientRunConnection_ContextCancellationClosesIdleSocket(t *testing.T) {
	fixture := newConnectionTestServer(t)
	client := newConnectionTestClient(t, fixture.server.URL)
	client.pingEvery = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- client.runConnection(ctx) }()

	select {
	case <-fixture.connected:
	case <-time.After(time.Second):
		t.Fatal("Runtime 未连接测试控制面")
	}
	cancel()
	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("context 取消没有关闭空闲 WebSocket")
	}
}
