package controlplane

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appconfig "github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/containerhost"
	"github.com/chenhg5/cc-connect/controlstore"
	"github.com/chenhg5/cc-connect/runtimeprotocol"
)

func TestControlAuthenticationSetupCookieCSRFAndLoginRateLimit(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	store, err := controlstore.Open(filepath.Join(directory, "control.db"), "setup-once")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	broker, err := NewBroker(store)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{PublicURL: "http://127.0.0.1:9820", ServerSocket: filepath.Join(directory, "server.sock"),
		RuntimeSocket: filepath.Join(directory, "runtime.sock"), AppDirectory: filepath.Join(directory, "app")}, store, broker)
	if err != nil {
		t.Fatal(err)
	}
	handler := server.publicHandler()
	installerRequest := httptest.NewRequest(http.MethodGet, "/runtime/v1/install.sh", nil)
	installerResponse := httptest.NewRecorder()
	handler.ServeHTTP(installerResponse, installerRequest)
	if installerResponse.Code != http.StatusOK || !strings.Contains(installerResponse.Body.String(), "set -euo pipefail") || installerResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("runtime installer response = %d, %q", installerResponse.Code, installerResponse.Body.String())
	}

	setupBody := []byte(`{"setup_token":"setup-once","username":"ccshus","password":"long-enough-password"}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/setup", bytes.NewReader(setupBody))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("setup without origin status = %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/setup", bytes.NewReader(setupBody))
	request.Header.Set("Origin", "http://127.0.0.1:9820")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("setup status = %d, body=%s", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("setup cookie = %#v", cookies)
	}
	var envelope struct {
		Data controlstore.Session `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || envelope.Data.CSRFToken == "" {
		t.Fatalf("setup session = %#v, %v", envelope, err)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/auth/profile", nil)
	request.AddCookie(cookies[0])
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"username":"ccshus"`) {
		t.Fatalf("profile response = %d, body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/devices/pairing-code", nil)
	request.AddCookie(cookies[0])
	request.Header.Set("Origin", "http://127.0.0.1:9820")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unsafe request without csrf status = %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/devices/pairing-code", nil)
	request.AddCookie(cookies[0])
	request.Header.Set("Origin", "https://forged.example")
	request.Header.Set("X-CSRF-Token", envelope.Data.CSRFToken)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("unsafe request with forged origin status = %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/devices/pairing-code", nil)
	request.AddCookie(cookies[0])
	request.Header.Set("Origin", "http://127.0.0.1:9820")
	request.Header.Set("X-CSRF-Token", envelope.Data.CSRFToken)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("csrf-authenticated request status = %d, body=%s", response.Code, response.Body.String())
	}

	for attempt := 0; attempt < 6; attempt++ {
		request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader([]byte(`{"username":"ccshus","password":"wrong-password"}`)))
		request.RemoteAddr = "203.0.113.7:1234"
		request.Header.Set("Origin", "http://127.0.0.1:9820")
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		expected := http.StatusUnauthorized
		if attempt == 5 {
			expected = http.StatusTooManyRequests
		}
		if response.Code != expected {
			t.Fatalf("login attempt %d status = %d, want %d", attempt+1, response.Code, expected)
		}
	}
	if required, err := store.SetupRequired(ctx); err != nil || required {
		t.Fatalf("setup state = %v, %v", required, err)
	}
}

func TestDeployDashboardPublishesContainerLifecycleCapabilities(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	store, err := controlstore.Open(filepath.Join(directory, "control.db"), "setup")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	broker, err := NewBroker(store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = broker.Close() })
	server, err := New(Config{PublicURL: "http://127.0.0.1:9820", ServerSocket: filepath.Join(directory, "server.sock"),
		RuntimeSocket: filepath.Join(directory, "runtime.sock"), AppDirectory: filepath.Join(directory, "app")}, store, broker)
	if err != nil {
		t.Fatal(err)
	}
	supervisor, err := NewSupervisor(SupervisorConfig{
		Binary: "/usr/local/bin/cc-connect-server", ConfigPath: filepath.Join(directory, "app", "config.toml"),
		ServerSocket: filepath.Join(directory, "server.sock"), RuntimeSocket: filepath.Join(directory, "runtime.sock"),
		LogDirectory: filepath.Join(directory, "logs"),
	})
	if err != nil {
		t.Fatal(err)
	}
	host := &deploymentContainerHost{status: containerhost.Status{
		CurrentTag: "v0.1.1", CurrentImage: "ghcr.io/shusfun/cc-connect@sha256:" + strings.Repeat("1", 64),
	}}
	manager, err := NewDeploymentManager(DeploymentConfig{
		Owner: DeploymentOwnerContainer, RunningVersion: "v0.1.1", ContainerHost: host,
		ControlDatabase: filepath.Join(directory, "control.db"), ActivationPath: filepath.Join(directory, "container-activation.json"),
	}, store, broker, supervisor)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.RegisterCurrent(ctx); err != nil {
		t.Fatal(err)
	}
	server.SetSupervisor(supervisor)
	server.SetDeploymentManager(manager)

	response := httptest.NewRecorder()
	server.handleDeployDashboard(response, httptest.NewRequest(http.MethodGet, "/api/v1/deploy/dashboard", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("dashboard status = %d, body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data struct {
			Deployment DeploymentCapabilities `json:"deployment"`
			CurrentTag string                 `json:"current_release_tag"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.CurrentTag != "v0.1.1" || envelope.Data.Deployment.Owner != DeploymentOwnerContainer ||
		!envelope.Data.Deployment.Available || !envelope.Data.Deployment.Update || !envelope.Data.Deployment.Rollback || !envelope.Data.Deployment.Restart {
		t.Fatalf("dashboard data = %#v", envelope.Data)
	}
}

func TestDecodeRequestRejectsUnknownTrailingAndOversizedBodies(t *testing.T) {
	tests := []struct {
		name string
		body []byte
		want string
	}{
		{name: "unknown field", body: []byte(`{"name":"ok","cwd":"/tmp"}`), want: `unknown field "cwd"`},
		{name: "trailing json", body: []byte(`{"name":"ok"}{"name":"second"}`), want: "multiple JSON values"},
		{name: "oversized", body: bytes.Repeat([]byte("x"), (50<<20)+1), want: "exceeds 50 MiB"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/", bytes.NewReader(test.body))
			var target struct {
				Name string `json:"name"`
			}
			err := decodeRequest(request, &target)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("decodeRequest() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestWriteInitialServerConfigCreatesWorkspaceOnlyServer(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.toml")
	err := writeInitialServerConfig(path, directory, serverConfigurationRequest{
		Language: "zh", EnableWeCom: true, WeComBotID: "bot-id", WeComSecret: "bot-secret", AllowFrom: "user-1",
	})
	if err != nil {
		t.Fatalf("writeInitialServerConfig() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %v, want 0600", info.Mode().Perm())
	}
	config, err := appconfig.Load(path)
	if err != nil {
		t.Fatalf("generated config is invalid: %v", err)
	}
	if len(config.Projects) != 0 || config.WorkspaceChat.Enabled == nil || !*config.WorkspaceChat.Enabled {
		t.Fatalf("generated config = %#v", config)
	}
	if config.WorkspaceChat.WeCom.BotSecret != "bot-secret" || len(config.WorkspaceChat.Transports) != 2 {
		t.Fatalf("generated workspace chat config = %#v", config.WorkspaceChat)
	}
}

func TestValidatePublicURLRequiresBareHTTPSOrigin(t *testing.T) {
	if got, err := validatePublicURL("https://cc.example.com/"); err != nil || got != "https://cc.example.com" {
		t.Fatalf("validatePublicURL(valid) = %q, %v", got, err)
	}
	for _, invalid := range []string{"http://cc.example.com", "https://user@cc.example.com", "https://cc.example.com/path", "https://cc.example.com?x=1"} {
		if _, err := validatePublicURL(invalid); err == nil {
			t.Fatalf("validatePublicURL(%q) unexpectedly succeeded", invalid)
		}
	}
}

func TestBrokerEventOverflowClosesSubscriptionForResync(t *testing.T) {
	broker := &Broker{eventSubs: make(map[uint64]*eventSubscription)}
	events, cancel := broker.SubscribeEvents()
	defer cancel()

	for sequence := uint64(1); sequence <= 257; sequence++ {
		broker.publishRuntimeEvent(runtimeprotocol.Envelope{Sequence: sequence})
	}
	for range 256 {
		if _, open := <-events; !open {
			t.Fatal("subscription closed before buffered events were drained")
		}
	}
	if _, open := <-events; open {
		t.Fatal("overflowed subscription remained open; caller cannot detect the resync boundary")
	}
	broker.mu.RLock()
	remaining := len(broker.eventSubs)
	broker.mu.RUnlock()
	if remaining != 0 {
		t.Fatalf("event subscribers = %d, want 0", remaining)
	}
}

func TestEventSubscriptionCloseIsIdempotent(t *testing.T) {
	subscription := &eventSubscription{events: make(chan runtimeprotocol.Envelope, 1)}
	subscription.close()
	subscription.close()
	if subscription.publish(runtimeprotocol.Envelope{}) {
		t.Fatal("closed subscription accepted an event")
	}
	if _, open := <-subscription.events; open {
		t.Fatal("closed subscription channel is open")
	}
}

func TestBrokerConnectionGenerationContinuesAfterControlRestart(t *testing.T) {
	ctx := context.Background()
	store, err := controlstore.Open(filepath.Join(t.TempDir(), "control.db"), "setup")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	code, err := store.CreatePairingCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	device, err := store.PairDevice(ctx, code.Code, "Mac", publicKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRuntimeCheckpoint(ctx, device.ID, 7, 12); err != nil {
		t.Fatal(err)
	}
	broker, err := NewBroker(store)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = broker.Close() }()
	first, err := broker.nextGeneration(ctx, device.ID)
	if err != nil || first != 8 {
		t.Fatalf("首个恢复代际 = %d, err=%v, want 8", first, err)
	}
	second, err := broker.nextGeneration(ctx, device.ID)
	if err != nil || second != 9 {
		t.Fatalf("后续代际 = %d, err=%v, want 9", second, err)
	}
}

func TestBrokerStrictJSONRejectsTrailingValue(t *testing.T) {
	var target map[string]any
	if err := strictJSON([]byte(`{} {}`), &target); err == nil {
		t.Fatal("control broker 接受了尾随 JSON")
	}
}

func TestBrokerRevokeDeviceClosesActiveConnection(t *testing.T) {
	ctx := context.Background()
	store, err := controlstore.Open(filepath.Join(t.TempDir(), "control.db"), "setup")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	code, err := store.CreatePairingCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	device, err := store.PairDevice(ctx, code.Code, "Mac", publicKey)
	if err != nil {
		t.Fatal(err)
	}
	broker, err := NewBroker(store)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = broker.Close() }()
	connection := &runtimeConnection{deviceID: device.ID, pending: make(map[string]chan runtimeprotocol.Envelope), closed: make(chan struct{})}
	broker.connections[device.ID] = connection

	if err := broker.RevokeDevice(ctx, device.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-connection.closed:
	default:
		t.Fatal("撤销设备后活动连接未关闭")
	}
	if broker.connections[device.ID] != nil {
		t.Fatal("撤销设备后 Broker 仍保留连接")
	}
	stored, err := store.Device(ctx, device.ID)
	if err != nil || stored.RevokedAt == nil {
		t.Fatalf("设备撤销状态 = %#v, err=%v", stored, err)
	}
}

func TestBrokerCatalogKeepsIdenticalLocalRefsIsolatedByDevice(t *testing.T) {
	ctx := context.Background()
	store, err := controlstore.Open(filepath.Join(t.TempDir(), "control.db"), "setup")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	broker, err := NewBroker(store)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = broker.Close() }()
	for _, name := range []string{"Mac 1", "Mac 2"} {
		code, err := store.CreatePairingCode(ctx)
		if err != nil {
			t.Fatal(err)
		}
		publicKey, _, err := ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatal(err)
		}
		device, err := store.PairDevice(ctx, code.Code, name, publicKey)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(runtimeprotocol.Catalog{Workspaces: []runtimeprotocol.Workspace{{
			LocalRef: "same-local-ref", ProjectID: "project", ProjectName: "Project", Available: true,
		}}})
		if err := broker.persistCatalog(device.ID, raw); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := store.ListCatalog(ctx)
	if err != nil || len(entries) != 2 {
		t.Fatalf("catalog = %#v, err=%v", entries, err)
	}
	if entries[0].GlobalRef == entries[1].GlobalRef {
		t.Fatal("不同设备的相同本地引用生成了相同全局 workspaceRef")
	}
}
