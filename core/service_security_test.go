package core

import (
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestServiceURLCredentialsRejected(t *testing.T) {
	checks := map[string]func(*http.Request) bool{
		"webhook":    NewWebhookServer(0, "secret", "").authenticate,
		"management": NewManagementServer(0, "secret", nil).authenticate,
		"bridge":     NewBridgeServer(0, "secret", "", nil).authenticate,
	}
	for name, authenticate := range checks {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/?token=secret", nil)
			if authenticate(request) {
				t.Fatal("credentials in URLs must be refused")
			}
			request.Header.Set("Authorization", "Bearer secret")
			if authenticate(request) {
				t.Fatal("a valid header must not make a credential-bearing URL acceptable")
			}
			request.URL.RawQuery = ""
			if !authenticate(request) {
				t.Fatal("valid credentials in a header must continue to work")
			}
		})
	}
}

func TestBridgeBrowserCredentialsUseSubprotocol(t *testing.T) {
	_, address := startTestBridge(t, "secret")
	dialer := websocket.Dialer{Subprotocols: []string{"cc-connect", "cc-connect-token.c2VjcmV0"}}
	connection, _, err := dialer.Dial(address, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if connection.Subprotocol() != "cc-connect" {
		t.Fatalf("credential must not be echoed: %q", connection.Subprotocol())
	}
	register(t, connection, "browser", []string{"text"})
}

func TestBridgeBoundsConnectionsBeforeRegistration(t *testing.T) {
	bridge, address := startTestBridge(t, "secret")
	headers := http.Header{"Authorization": {"Bearer secret"}}
	for i := 0; i < 16; i++ {
		connection, _, err := websocket.DefaultDialer.Dial(address, headers)
		if err != nil {
			t.Fatal(err)
		}
		defer connection.Close()
	}
	connection, response, err := websocket.DefaultDialer.Dial(address, headers)
	if err == nil {
		connection.Close()
		t.Fatal("seventeenth connection accepted")
	}
	if response == nil || response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("response=%v error=%v", response, err)
	}
	bridge.Stop()
	{
		// Stop closes pending registration sockets as well as registered adapters.
		deadline := time.Now().Add(time.Second)
		for bridge.activeConnections.Load() != 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if bridge.activeConnections.Load() != 0 {
			t.Fatal("pending registration sockets survived Stop")
		}
	}
}

func TestBridgeTrafficBudget(t *testing.T) {
	now := time.Now()
	budget := bridgeBudget{since: now}
	if !budget.take(64<<20, now) || budget.take(1, now) {
		t.Fatal("byte limit not enforced")
	}
	if !budget.take(1, now.Add(time.Minute)) {
		t.Fatal("budget did not reset")
	}
	budget = bridgeBudget{since: now}
	for i := 0; i < 600; i++ {
		if !budget.take(0, now) {
			t.Fatalf("message %d rejected", i)
		}
	}
	if budget.take(0, now) {
		t.Fatal("message limit not enforced")
	}
}

func TestInsecureBridgeRejectsRemoteRequestsEvenWhenProxied(t *testing.T) {
	bridge := NewBridgeServerInsecure(0, "", "", nil)
	request := httptest.NewRequest(http.MethodGet, "/bridge/ws", nil)
	request.RemoteAddr = "10.1.2.3:1234"
	request.Header.Set("X-Forwarded-For", "127.0.0.1")
	if bridge.authenticate(request) {
		t.Fatal("private management proxy must not expose an insecure bridge")
	}
	request.RemoteAddr = "127.0.0.1:1234"
	if !bridge.authenticate(request) {
		t.Fatal("explicit local development must still work")
	}
}

func TestBridgeStopRejectsNewSockets(t *testing.T) {
	bridge := NewBridgeServer(0, "secret", "", nil)
	bridge.Stop()
	server := httptest.NewServer(http.HandlerFunc(bridge.handleWS))
	defer server.Close()
	address := "ws" + server.URL[len("http"):]
	socket, response, err := websocket.DefaultDialer.Dial(address, http.Header{"Authorization": {"Bearer secret"}})
	if err == nil {
		socket.Close()
		t.Fatal("stopped bridge accepted a new socket")
	}
	if response == nil || response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("response=%v error=%v", response, err)
	}
}
