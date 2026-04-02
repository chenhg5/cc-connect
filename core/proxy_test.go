package core

import (
	"net/http"
	"testing"
	"time"
)

func TestBuildHTTPClient_NoProxy(t *testing.T) {
	client, err := BuildHTTPClient(nil, 60*time.Second)
	if err != nil {
		t.Fatalf("BuildHTTPClient(nil) error = %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if client.Timeout != 60*time.Second {
		t.Errorf("Timeout = %v, want %v", client.Timeout, 60*time.Second)
	}
}

func TestBuildHTTPClient_HTTPProxy(t *testing.T) {
	cfg := &ProxyConfig{
		Type: "http",
		Addr: "127.0.0.1:8080",
	}

	client, err := BuildHTTPClient(cfg, 60*time.Second)
	if err != nil {
		t.Fatalf("BuildHTTPClient() error = %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil client")
	}

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("expected *http.Transport, got different type")
	}

	// Verify proxy is set by making a request
	req, _ := http.NewRequest("GET", "http://example.com", nil)
	proxyURL, err := transport.Proxy(req)
	if err != nil {
		t.Fatalf("Proxy() error = %v", err)
	}
	if proxyURL == nil {
		t.Fatal("expected non-nil proxy URL")
	}
	if proxyURL.Host != "127.0.0.1:8080" {
		t.Errorf("Proxy URL host = %s, want 127.0.0.1:8080", proxyURL.Host)
	}
}

func TestBuildHTTPClient_HTTPProxyWithAuth(t *testing.T) {
	cfg := &ProxyConfig{
		Type:     "http",
		Addr:     "proxy.example.com:3128",
		Username: "user",
		Password: "pass",
	}

	client, err := BuildHTTPClient(cfg, 60*time.Second)
	if err != nil {
		t.Fatalf("BuildHTTPClient() error = %v", err)
	}

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("expected *http.Transport, got different type")
	}

	req, _ := http.NewRequest("GET", "http://example.com", nil)
	proxyURL, err := transport.Proxy(req)
	if err != nil {
		t.Fatalf("Proxy() error = %v", err)
	}
	if proxyURL == nil {
		t.Fatal("expected non-nil proxy URL")
	}
	if proxyURL.User == nil {
		t.Fatal("expected proxy URL with user info")
	}
	if user := proxyURL.User.Username(); user != "user" {
		t.Errorf("Proxy username = %s, want user", user)
	}
	if pass, ok := proxyURL.User.Password(); !ok || pass != "pass" {
		t.Errorf("Proxy password = %s, want pass", pass)
	}
}

func TestBuildHTTPClient_HTTPProxyWithURLScheme(t *testing.T) {
	cfg := &ProxyConfig{
		Type: "http",
		Addr: "http://proxy.example.com:8080",
	}

	client, err := BuildHTTPClient(cfg, 60*time.Second)
	if err != nil {
		t.Fatalf("BuildHTTPClient() error = %v", err)
	}

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("expected *http.Transport, got different type")
	}

	req, _ := http.NewRequest("GET", "http://example.com", nil)
	proxyURL, err := transport.Proxy(req)
	if err != nil {
		t.Fatalf("Proxy() error = %v", err)
	}
	if proxyURL == nil {
		t.Fatal("expected non-nil proxy URL")
	}
	if proxyURL.Host != "proxy.example.com:8080" {
		t.Errorf("Proxy URL host = %s, want proxy.example.com:8080", proxyURL.Host)
	}
}

func TestBuildHTTPClient_HTTPSProxy(t *testing.T) {
	cfg := &ProxyConfig{
		Type: "https",
		Addr: "secure-proxy.example.com:8443",
	}

	client, err := BuildHTTPClient(cfg, 60*time.Second)
	if err != nil {
		t.Fatalf("BuildHTTPClient() error = %v", err)
	}

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("expected *http.Transport, got different type")
	}

	req, _ := http.NewRequest("GET", "http://example.com", nil)
	proxyURL, err := transport.Proxy(req)
	if err != nil {
		t.Fatalf("Proxy() error = %v", err)
	}
	if proxyURL == nil {
		t.Fatal("expected non-nil proxy URL")
	}
	if proxyURL.Scheme != "https" {
		t.Errorf("Proxy URL scheme = %s, want https", proxyURL.Scheme)
	}
}

func TestBuildHTTPClient_SOCKS5Proxy(t *testing.T) {
	cfg := &ProxyConfig{
		Type: "socks5",
		Addr: "127.0.0.1:1080",
	}

	client, err := BuildHTTPClient(cfg, 60*time.Second)
	if err != nil {
		t.Fatalf("BuildHTTPClient() error = %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil client")
	}

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("expected *http.Transport, got different type")
	}
	if transport.DialContext == nil {
		t.Fatal("expected custom DialContext for SOCKS5")
	}
}

func TestBuildHTTPClient_SOCKS5ProxyWithScheme(t *testing.T) {
	cfg := &ProxyConfig{
		Type: "socks5",
		Addr: "socks5://127.0.0.1:1080",
	}

	client, err := BuildHTTPClient(cfg, 60*time.Second)
	if err != nil {
		t.Fatalf("BuildHTTPClient() error = %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestBuildHTTPClient_SOCKS5ProxyWithAuth(t *testing.T) {
	cfg := &ProxyConfig{
		Type:     "socks5",
		Addr:     "127.0.0.1:1080",
		Username: "socksuser",
		Password: "sockspass",
	}

	client, err := BuildHTTPClient(cfg, 60*time.Second)
	if err != nil {
		t.Fatalf("BuildHTTPClient() error = %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestBuildHTTPClient_UnsupportedProxyType(t *testing.T) {
	cfg := &ProxyConfig{
		Type: "ftp",
		Addr: "proxy.example.com:2121",
	}

	_, err := BuildHTTPClient(cfg, 60*time.Second)
	if err == nil {
		t.Fatal("expected error for unsupported proxy type, got nil")
	}
}

func TestBuildHTTPClient_EmptyProxyAddr(t *testing.T) {
	cfg := &ProxyConfig{
		Type: "http",
		Addr: "",
	}

	client, err := BuildHTTPClient(cfg, 60*time.Second)
	if err != nil {
		t.Fatalf("BuildHTTPClient() error = %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestBuildHTTPClient_InvalidProxyURL(t *testing.T) {
	cfg := &ProxyConfig{
		Type: "http",
		Addr: "://invalid-url",
	}

	_, err := BuildHTTPClient(cfg, 60*time.Second)
	if err == nil {
		t.Fatal("expected error for invalid proxy URL, got nil")
	}
}

func TestBuildHTTPClient_CustomTimeout(t *testing.T) {
	cfg := &ProxyConfig{
		Type: "http",
		Addr: "127.0.0.1:8080",
	}

	timeout := 30 * time.Second
	client, err := BuildHTTPClient(cfg, timeout)
	if err != nil {
		t.Fatalf("BuildHTTPClient() error = %v", err)
	}
	if client.Timeout != timeout {
		t.Errorf("Timeout = %v, want %v", client.Timeout, timeout)
	}
}

func TestBuildHTTPClient_ProxyFromConfig(t *testing.T) {
	tests := []struct {
		name         string
		proxyType    string
		addr         string
		username     string
		password     string
		expectProxy  bool
		expectAuth   bool
		expectedHost string
	}{
		{
			name:         "HTTP proxy without auth",
			proxyType:    "http",
			addr:         "proxy1.example.com:8080",
			expectProxy:  true,
			expectAuth:   false,
			expectedHost: "proxy1.example.com:8080",
		},
		{
			name:         "HTTP proxy with auth",
			proxyType:    "http",
			addr:         "proxy2.example.com:3128",
			username:     "testuser",
			password:     "testpass",
			expectProxy:  true,
			expectAuth:   true,
			expectedHost: "proxy2.example.com:3128",
		},
		{
			name:         "HTTPS proxy",
			proxyType:    "https",
			addr:         "secure-proxy.example.com:8443",
			expectProxy:  true,
			expectAuth:   false,
			expectedHost: "secure-proxy.example.com:8443",
		},
		{
			name:         "SOCKS5 proxy",
			proxyType:    "socks5",
			addr:         "socks5-proxy.example.com:1080",
			expectProxy:  true,
			expectAuth:   false,
			expectedHost: "", // SOCKS5 doesn't use standard proxy URL
		},
		{
			name:        "No proxy (empty addr)",
			proxyType:   "http",
			addr:        "",
			expectProxy: false,
		},
		{
			name:        "No proxy (nil config)",
			proxyType:   "",
			addr:        "",
			expectProxy: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg *ProxyConfig
			if tt.proxyType != "" || tt.addr != "" {
				cfg = &ProxyConfig{
					Type:     tt.proxyType,
					Addr:     tt.addr,
					Username: tt.username,
					Password: tt.password,
				}
			}

			client, err := BuildHTTPClient(cfg, 60*time.Second)
			if err != nil {
				t.Fatalf("BuildHTTPClient() error = %v", err)
			}
			if client == nil {
				t.Fatal("expected non-nil client")
			}

			if !tt.expectProxy {
				// No proxy expected, transport should be nil or default
				if client.Transport != nil {
					transport, ok := client.Transport.(*http.Transport)
					if ok && transport.Proxy != nil {
						req, _ := http.NewRequest("GET", "http://example.com", nil)
						proxyURL, err := transport.Proxy(req)
						if err == nil && proxyURL != nil {
							t.Errorf("expected no proxy, got %v", proxyURL)
						}
					}
				}
				return
			}

			// Proxy expected
			if tt.proxyType == "socks5" {
				// SOCKS5 uses custom dialer
				if client.Transport == nil {
					t.Fatal("expected transport for SOCKS5 proxy")
				}
				return
			}

			// HTTP/HTTPS proxy
			transport, ok := client.Transport.(*http.Transport)
			if !ok {
				t.Fatalf("expected *http.Transport, got %T", client.Transport)
			}
			if transport.Proxy == nil {
				t.Fatal("expected non-nil Proxy function")
			}

			req, _ := http.NewRequest("GET", "http://example.com", nil)
			proxyURL, err := transport.Proxy(req)
			if err != nil {
				t.Fatalf("Proxy() error = %v", err)
			}
			if proxyURL == nil {
				t.Fatal("expected non-nil proxy URL")
			}
			if proxyURL.Host != tt.expectedHost {
				t.Errorf("Proxy URL host = %s, want %s", proxyURL.Host, tt.expectedHost)
			}

			if tt.expectAuth {
				if proxyURL.User == nil {
					t.Fatal("expected proxy URL with user info")
				}
				if user := proxyURL.User.Username(); user != tt.username {
					t.Errorf("Proxy username = %s, want %s", user, tt.username)
				}
				if pass, ok := proxyURL.User.Password(); !ok || pass != tt.password {
					t.Errorf("Proxy password = %s, want %s", pass, tt.password)
				}
			}
		})
	}
}

func TestBuildHTTPClient_ProxyURLParsing(t *testing.T) {
	tests := []struct {
		name         string
		addr         string
		expectedHost string
		expectedErr  bool
	}{
		{
			name:         "Host:Port format",
			addr:         "proxy.example.com:8080",
			expectedHost: "proxy.example.com:8080",
			expectedErr:  false,
		},
		{
			name:         "IPv4:Port format",
			addr:         "192.168.1.1:3128",
			expectedHost: "192.168.1.1:3128",
			expectedErr:  false,
		},
		{
			name:         "IPv6:Port format",
			addr:         "[::1]:8080",
			expectedHost: "[::1]:8080",
			expectedErr:  false,
		},
		{
			name:         "Full URL format",
			addr:         "http://proxy.example.com:8080",
			expectedHost: "proxy.example.com:8080",
			expectedErr:  false,
		},
		{
			name:         "HTTPS URL format",
			addr:         "https://proxy.example.com:8443",
			expectedHost: "proxy.example.com:8443",
			expectedErr:  false,
		},
		{
			name:         "Invalid URL",
			addr:         "http://proxy example.com:8080",
			expectedHost: "",
			expectedErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &ProxyConfig{
				Type: "http",
				Addr: tt.addr,
			}

			client, err := BuildHTTPClient(cfg, 60*time.Second)
			if tt.expectedErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if client == nil {
				t.Fatal("expected non-nil client")
			}

			transport, ok := client.Transport.(*http.Transport)
			if !ok {
				t.Fatal("expected *http.Transport")
			}

			req, _ := http.NewRequest("GET", "http://example.com", nil)
			proxyURL, err := transport.Proxy(req)
			if err != nil {
				t.Fatalf("Proxy() error = %v", err)
			}
			if proxyURL == nil {
				t.Fatal("expected non-nil proxy URL")
			}
			if proxyURL.Host != tt.expectedHost {
				t.Errorf("Proxy URL host = %s, want %s", proxyURL.Host, tt.expectedHost)
			}
		})
	}
}
