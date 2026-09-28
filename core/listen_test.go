package core

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Bind normalization
// ---------------------------------------------------------------------------

func TestNormalizeBind(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty defaults to loopback", "", "127.0.0.1"},
		{"blank defaults to loopback", "   ", "127.0.0.1"},
		{"trimmed", " 0.0.0.0 ", "0.0.0.0"},
		{"ipv4 loopback", "127.0.0.1", "127.0.0.1"},
		{"wildcard", "0.0.0.0", "0.0.0.0"},
		{"bracketed ipv6 wildcard is unwrapped", "[::]", "::"},
		{"bare ipv6 wildcard", "::", "::"},
		{"bracketed ipv6 loopback is unwrapped", "[::1]", "::1"},
		{"bare ipv6 loopback", "::1", "::1"},
		{"hostname", "localhost", "localhost"},
		{"brackets that are not an ip are kept", "[fe80::1%eth0]", "[fe80::1%eth0]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeBind(tc.in); got != tc.want {
				t.Fatalf("normalizeBind(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestListenAddressNeverDoubleBracketsIPv6 is the regression for the review
// finding that bind="[::]" produced the invalid "[[::]]:port".
func TestListenAddressNeverDoubleBracketsIPv6(t *testing.T) {
	cases := []struct {
		bind string
		port int
		want string
	}{
		{"", 9820, "127.0.0.1:9820"},
		{"0.0.0.0", 9820, "0.0.0.0:9820"},
		{"[::]", 9820, "[::]:9820"},
		{"::", 9820, "[::]:9820"},
		{"[::1]", 9820, "[::1]:9820"},
		{"::1", 9820, "[::1]:9820"},
	}
	for _, tc := range cases {
		if got := listenAddress(tc.bind, tc.port); got != tc.want {
			t.Fatalf("listenAddress(%q, %d) = %q, want %q", tc.bind, tc.port, got, tc.want)
		}
		if strings.Contains(listenAddress(tc.bind, tc.port), "[[") {
			t.Fatalf("listenAddress(%q, %d) = %q contains a doubled bracket", tc.bind, tc.port, listenAddress(tc.bind, tc.port))
		}
	}
}

// TestListenAddressForIPv6BindIsDialable proves the normalized address is a
// valid host:port pair that net.Listen can actually use.
func TestListenAddressForIPv6BindIsDialable(t *testing.T) {
	addr := listenAddress("[::1]", 0)
	if _, _, err := net.SplitHostPort(addr); err != nil {
		t.Fatalf("listenAddress(\"[::1]\", 0) = %q is not host:port: %v", addr, err)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("IPv6 loopback unavailable on this host: %v", err)
	}
	defer ln.Close()
	if _, _, err := net.SplitHostPort(ln.Addr().String()); err != nil {
		t.Fatalf("bound address %q is not host:port: %v", ln.Addr(), err)
	}
}

func TestIsLoopbackBind(t *testing.T) {
	cases := []struct {
		bind string
		want bool
	}{
		{"", true},
		{"127.0.0.1", true},
		{"127.0.0.53", true},
		{"[::1]", true},
		{"::1", true},
		{"localhost", true},
		{"LOCALHOST", true},
		{"0.0.0.0", false},
		{"[::]", false},
		{"::", false},
		{"192.168.1.10", false},
		{"example.com", false},
	}
	for _, tc := range cases {
		if got := isLoopbackBind(tc.bind); got != tc.want {
			t.Fatalf("isLoopbackBind(%q) = %v, want %v", tc.bind, got, tc.want)
		}
	}
}

func TestCheckRemoteBindAuth(t *testing.T) {
	cases := []struct {
		name    string
		bind    string
		token   string
		wantErr bool
	}{
		{"loopback without token is allowed", "", "", false},
		{"loopback ip without token is allowed", "127.0.0.1", "", false},
		{"loopback v6 without token is allowed", "[::1]", "", false},
		{"wildcard without token is refused", "0.0.0.0", "", true},
		{"v6 wildcard without token is refused", "[::]", "", true},
		{"nic address without token is refused", "10.0.0.5", "", true},
		{"hostname without token is refused", "my-host.internal", "", true},
		{"wildcard with token is allowed", "0.0.0.0", "secret", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkRemoteBindAuth("management", tc.bind, tc.token)
			if tc.wantErr && err == nil {
				t.Fatalf("checkRemoteBindAuth(%q, %q) = nil, want error", tc.bind, tc.token)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("checkRemoteBindAuth(%q, %q) = %v, want nil", tc.bind, tc.token, err)
			}
			if err != nil && !strings.Contains(err.Error(), "non-loopback") {
				t.Fatalf("error %q does not explain the non-loopback refusal", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Socket-level helpers
// ---------------------------------------------------------------------------

// freePort reserves an ephemeral port and releases it, returning the number.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("release port: %v", err)
	}
	return port
}

// requireNothingListening fails when any socket is still accepting on port.
func requireNothingListening(t *testing.T, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", net.JoinHostPort("0.0.0.0", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("port %d is still bound after a refused start: %v", port, err)
	}
	_ = ln.Close()
}

func requireLoopbackHost(t *testing.T, addr string) int {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("address %q is not host:port: %v", addr, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		t.Fatalf("listener address %q is not loopback", addr)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("address %q has a non-numeric port: %v", addr, err)
	}
	return port
}

func requireUnspecifiedHost(t *testing.T, addr string) {
	t.Helper()
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("address %q is not host:port: %v", addr, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsUnspecified() {
		t.Fatalf("listener address %q is not a wildcard bind", addr)
	}
}

func dialTCP(t *testing.T, addr string) (net.Conn, error) {
	t.Helper()
	return net.DialTimeout("tcp", addr, 2*time.Second)
}

// ---------------------------------------------------------------------------
// Management server: default loopback, explicit remote, refusal, bind errors
// ---------------------------------------------------------------------------

func TestManagementServer_DefaultBindIsLoopback(t *testing.T) {
	mgmt := NewManagementServer(0, "tok", nil)
	if err := mgmt.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(mgmt.Stop)

	port := requireLoopbackHost(t, mgmt.Addr())

	conn, err := dialTCP(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("dial the loopback listener: %v", err)
	}
	_ = conn.Close()
}

func TestManagementServer_ExplicitBindAllowsRemoteAccess(t *testing.T) {
	mgmt := NewManagementServerWithBind("0.0.0.0", 0, "tok", nil)
	if err := mgmt.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(mgmt.Stop)

	requireUnspecifiedHost(t, mgmt.Addr())

	// A wildcard listener must still serve the loopback path through the real
	// HTTP handler.
	_, portStr, err := net.SplitHostPort(mgmt.Addr())
	if err != nil {
		t.Fatalf("split address: %v", err)
	}
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+portStr+"/api/v1/status", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("request the wildcard listener over loopback: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

// TestManagementServer_RefusesRemoteBindWithoutToken is the regression for
// "management still accepts 0.0.0.0 with an empty token and then allows all API
// requests".
func TestManagementServer_RefusesRemoteBindWithoutToken(t *testing.T) {
	port := freePort(t)
	mgmt := NewManagementServerWithBind("0.0.0.0", port, "", nil)

	err := mgmt.Start()
	if err == nil {
		t.Cleanup(mgmt.Stop)
		t.Fatal("Start() = nil, want an error for an unauthenticated non-loopback bind")
	}
	if !strings.Contains(err.Error(), "without a token") {
		t.Fatalf("error %q does not mention the missing token", err)
	}
	if mgmt.Addr() != "" {
		t.Fatalf("Addr() = %q after a refused start, want empty", mgmt.Addr())
	}
	requireNothingListening(t, port)
}

func TestManagementServer_AllowsIPv6WildcardWithToken(t *testing.T) {
	mgmt := NewManagementServerWithBind("[::]", 0, "tok", nil)
	if err := mgmt.Start(); err != nil {
		t.Skipf("IPv6 unavailable on this host: %v", err)
	}
	t.Cleanup(mgmt.Stop)

	host, _, err := net.SplitHostPort(mgmt.Addr())
	if err != nil {
		t.Fatalf("address %q is not host:port: %v", mgmt.Addr(), err)
	}
	if strings.Contains(host, "[") {
		t.Fatalf("listener address %q kept the brackets from bind=[::]", mgmt.Addr())
	}
}

// TestManagementServer_StartReportsPortInUse is the regression for "listen
// failures happen inside a goroutine, so startup still reports success".
func TestManagementServer_StartReportsPortInUse(t *testing.T) {
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy a port: %v", err)
	}
	defer blocker.Close()
	port := blocker.Addr().(*net.TCPAddr).Port

	mgmt := NewManagementServer(port, "tok", nil)
	if err := mgmt.Start(); err == nil {
		t.Cleanup(mgmt.Stop)
		t.Fatal("Start() = nil, want an error when the port is already in use")
	}
	if mgmt.Addr() != "" {
		t.Fatalf("Addr() = %q after a failed start, want empty", mgmt.Addr())
	}
}

func TestManagementServer_StartReportsInvalidBindAddress(t *testing.T) {
	mgmt := NewManagementServerWithBind("not a host", 0, "tok", nil)
	if err := mgmt.Start(); err == nil {
		t.Cleanup(mgmt.Stop)
		t.Fatal("Start() = nil, want an error for an invalid bind address")
	}
}

// TestManagementServer_LoopbackBindNotReachableViaNonLoopbackAddress proves the
// socket really is bound to loopback rather than to the wildcard.
func TestManagementServer_LoopbackBindNotReachableViaNonLoopbackAddress(t *testing.T) {
	remote := firstNonLoopbackIPv4(t)

	mgmt := NewManagementServer(0, "tok", nil)
	if err := mgmt.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(mgmt.Stop)
	port := requireLoopbackHost(t, mgmt.Addr())
	portStr := strconv.Itoa(port)

	if conn, err := dialTCP(t, net.JoinHostPort("127.0.0.1", portStr)); err != nil {
		t.Fatalf("loopback connection refused: %v", err)
	} else {
		_ = conn.Close()
	}

	conn, err := dialTCP(t, net.JoinHostPort(remote.String(), portStr))
	if err == nil {
		_ = conn.Close()
		t.Fatalf("management api accepted a connection on non-loopback address %s:%s", remote, portStr)
	}
}

func firstNonLoopbackIPv4(t *testing.T) net.IP {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Skipf("cannot enumerate interfaces: %v", err)
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipnet.IP.To4()
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		return ip
	}
	t.Skip("no non-loopback IPv4 address available")
	return nil
}

// ---------------------------------------------------------------------------
// Bridge server
// ---------------------------------------------------------------------------

func TestBridgeServer_DefaultBindIsLoopback(t *testing.T) {
	bs := NewBridgeServer(freePort(t), "tok", "/bridge/ws", nil)
	if err := bs.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(bs.Stop)

	port := requireLoopbackHost(t, bs.Addr())

	conn, err := dialTCP(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("dial the loopback listener: %v", err)
	}
	_ = conn.Close()
}

func TestBridgeServer_ExplicitBindAllowsRemoteAccess(t *testing.T) {
	bs := NewBridgeServerWithBind("0.0.0.0", freePort(t), "tok", "/bridge/ws", nil)
	if err := bs.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(bs.Stop)

	requireUnspecifiedHost(t, bs.Addr())

	_, portStr, err := net.SplitHostPort(bs.Addr())
	if err != nil {
		t.Fatalf("split address: %v", err)
	}
	// The REST side of the bridge is served and still requires the token.
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Get(
		"http://127.0.0.1:" + portStr + "/bridge/sessions?session_key=k")
	if err != nil {
		t.Fatalf("request the wildcard listener over loopback: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

// TestBridgeServer_RefusesInsecureRemoteBindWithoutToken is the regression for
// "bridge accepts wildcard bind + empty token + insecure=true + CORS * with only
// a generic warning".
func TestBridgeServer_RefusesInsecureRemoteBindWithoutToken(t *testing.T) {
	port := freePort(t)
	bs := NewBridgeServerInsecureWithBind("0.0.0.0", port, "", "/bridge/ws", []string{"*"})
	if bs == nil {
		t.Fatal("NewBridgeServerInsecureWithBind returned nil")
	}

	err := bs.Start()
	if err == nil {
		t.Cleanup(bs.Stop)
		t.Fatal("Start() = nil, want an error for insecure mode on a non-loopback bind without a token")
	}
	if !strings.Contains(err.Error(), "without a token") {
		t.Fatalf("error %q does not mention the missing token", err)
	}
	if bs.Addr() != "" {
		t.Fatalf("Addr() = %q after a refused start, want empty", bs.Addr())
	}
	requireNothingListening(t, port)
}

func TestBridgeServer_NonInsecureBindWithoutTokenIsRefused(t *testing.T) {
	if bs := NewBridgeServerWithBind("0.0.0.0", freePort(t), "", "/bridge/ws", nil); bs != nil {
		t.Fatalf("constructor returned a server for a tokenless non-insecure bind")
	}
}

// TestBridgeServer_InsecureLoopbackWithoutTokenStillStarts keeps the documented
// local-development affordance working.
func TestBridgeServer_InsecureLoopbackWithoutTokenStillStarts(t *testing.T) {
	bs := NewBridgeServerInsecureWithBind("127.0.0.1", freePort(t), "", "/bridge/ws", nil)
	if bs == nil {
		t.Fatal("NewBridgeServerInsecureWithBind returned nil")
	}
	if err := bs.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(bs.Stop)
	requireLoopbackHost(t, bs.Addr())
}

func TestBridgeServer_StartReportsPortInUse(t *testing.T) {
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy a port: %v", err)
	}
	defer blocker.Close()
	port := blocker.Addr().(*net.TCPAddr).Port

	bs := NewBridgeServer(port, "tok", "/bridge/ws", nil)
	if err := bs.Start(); err == nil {
		t.Cleanup(bs.Stop)
		t.Fatal("Start() = nil, want an error when the port is already in use")
	}
	if bs.Addr() != "" {
		t.Fatalf("Addr() = %q after a failed start, want empty", bs.Addr())
	}
}

// TestListenTCPReportsAddressInUse covers the synchronous-bind helper directly.
func TestListenTCPReportsAddressInUse(t *testing.T) {
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy a port: %v", err)
	}
	defer blocker.Close()
	port := blocker.Addr().(*net.TCPAddr).Port

	ln, addr, err := listenTCP("127.0.0.1", port)
	if err == nil {
		_ = ln.Close()
		t.Fatal("listenTCP() = nil, want an error for a port already in use")
	}
	if want := "127.0.0.1:" + strconv.Itoa(port); addr != want {
		t.Fatalf("listenTCP reported addr %q, want %q", addr, want)
	}
}
