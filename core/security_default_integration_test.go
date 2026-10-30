package core

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSecurityDefaultsRejectEmptyTokens(t *testing.T) {
	if NewWebhookServer(0, "", "/hook") != nil {
		t.Error("webhook accepted an empty token without explicit insecure opt-in")
	}
	if NewManagementServer(0, "", nil) != nil {
		t.Error("management API accepted an empty token without explicit insecure opt-in")
	}
}

func TestSecurityDefaultsBindAndHTTPBoundary(t *testing.T) {
	tests := []struct {
		name      string
		loopback  bool
		token     string
		newServer func(port int) (addr string, stop func())
		path      string
	}{
		{
			name: "webhook-insecure-loopback", loopback: true, path: "/hook",
			newServer: func(port int) (string, func()) {
				s := NewWebhookServerInsecure(port, "", "/hook")
				s.Start()
				return s.server.Addr, s.Stop
			},
		},
		{
			name: "webhook-token-all-interfaces", token: "secret", path: "/hook",
			newServer: func(port int) (string, func()) {
				s := NewWebhookServer(port, "secret", "/hook")
				s.Start()
				return s.server.Addr, s.Stop
			},
		},
		{
			name: "webhook-insecure-with-token-loopback", loopback: true, token: "secret", path: "/hook",
			newServer: func(port int) (string, func()) {
				s := NewWebhookServerInsecure(port, "secret", "/hook")
				s.Start()
				return s.server.Addr, s.Stop
			},
		},
		{
			name: "management-insecure-loopback", loopback: true, path: "/api/v1/status",
			newServer: func(port int) (string, func()) {
				s := NewManagementServerInsecure(port, "", nil)
				s.Start()
				return s.server.Addr, s.Stop
			},
		},
		{
			name: "management-token-all-interfaces", token: "secret", path: "/api/v1/status",
			newServer: func(port int) (string, func()) {
				s := NewManagementServer(port, "secret", nil)
				s.Start()
				return s.server.Addr, s.Stop
			},
		},
		{
			name: "management-insecure-with-token-loopback", loopback: true, token: "secret", path: "/api/v1/status",
			newServer: func(port int) (string, func()) {
				s := NewManagementServerInsecure(port, "secret", nil)
				s.Start()
				return s.server.Addr, s.Stop
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port, err := strconv.Atoi(strings.TrimPrefix(listener.Addr().String(), "127.0.0.1:"))
			if err != nil {
				t.Fatal(err)
			}
			_ = listener.Close()
			addr, stop := tc.newServer(port)
			t.Cleanup(stop)
			if tc.loopback && !strings.HasPrefix(addr, "127.0.0.1:") {
				t.Fatalf("insecure server bound %q; want IPv4 loopback", addr)
			}
			if !tc.loopback && !strings.HasPrefix(addr, ":") {
				t.Fatalf("token server bound %q; want configured all-interface behavior", addr)
			}
			method := http.MethodGet
			authorizedStatus := http.StatusOK
			if strings.Contains(tc.name, "webhook") {
				method = http.MethodPost
				authorizedStatus = http.StatusBadRequest // Body is valid JSON but lacks session_key.
			}
			client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: time.Second}
			request := func(token string) int {
				deadline := time.Now().Add(3 * time.Second)
				for {
					r, err := http.NewRequest(method, "http://127.0.0.1:"+strconv.Itoa(port)+tc.path, strings.NewReader("{}"))
					if err != nil {
						t.Fatal(err)
					}
					if token != "" {
						r.Header.Set("Authorization", "Bearer "+token)
					}
					resp, err := client.Do(r)
					if err == nil {
						_ = resp.Body.Close()
						return resp.StatusCode
					}
					if time.Now().After(deadline) {
						t.Fatalf("HTTP server %s did not accept loopback request: %v", addr, err)
					}
					time.Sleep(20 * time.Millisecond)
				}
			}
			if tc.token == "" {
				if got := request(""); got != authorizedStatus {
					t.Fatalf("explicit insecure mode returned %d; want handler status %d", got, authorizedStatus)
				}
			} else {
				if got := request(""); got != http.StatusUnauthorized {
					t.Fatalf("missing token returned %d; want 401", got)
				}
				if got := request("secret"); got != authorizedStatus {
					t.Fatalf("correct token returned %d; want handler status %d", got, authorizedStatus)
				}
			}
		})
	}
}
