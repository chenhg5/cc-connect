package core

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

// ProxyConfig holds proxy configuration for platform connections.
type ProxyConfig struct {
	Type     string // "http", "https", or "socks5"
	Addr     string // proxy address (e.g. "127.0.0.1:1080")
	Username string // optional proxy username
	Password string // optional proxy password
}

// BuildHTTPClient creates an http.Client with optional proxy support.
// If proxyCfg is nil, returns a client with direct connection.
func BuildHTTPClient(proxyCfg *ProxyConfig, timeout time.Duration) (*http.Client, error) {
	client := &http.Client{
		Timeout: timeout,
	}

	if proxyCfg == nil || proxyCfg.Addr == "" {
		return client, nil
	}

	switch strings.ToLower(proxyCfg.Type) {
	case "http", "https":
		proxyURL, err := buildHTTPProxyURL(proxyCfg)
		if err != nil {
			return nil, err
		}
		client.Transport = &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		}
		slog.Info("using HTTP proxy", "type", proxyCfg.Type, "addr", proxyURL.Host, "auth", proxyCfg.Username != "")

	case "socks5":
		dialer, err := buildSocks5Dialer(proxyCfg)
		if err != nil {
			return nil, fmt.Errorf("socks5: %w", err)
		}
		client.Transport = &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.Dial(network, addr)
			},
		}
		slog.Info("using SOCKS5 proxy", "addr", proxyCfg.Addr, "auth", proxyCfg.Username != "")

	default:
		return nil, fmt.Errorf("unsupported proxy type: %s (supported: http, https, socks5)", proxyCfg.Type)
	}

	return client, nil
}

// buildHTTPProxyURL constructs a proxy URL from config.
func buildHTTPProxyURL(cfg *ProxyConfig) (*url.URL, error) {
	scheme := cfg.Type
	if scheme == "http" {
		scheme = "http"
	}

	// Support both "host:port" and "protocol://host:port" formats
	proxyAddr := cfg.Addr
	if !strings.Contains(proxyAddr, "://") {
		proxyAddr = scheme + "://" + proxyAddr
	}

	proxyURL, err := url.Parse(proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy URL %q: %w", proxyAddr, err)
	}

	if cfg.Username != "" {
		proxyURL.User = url.UserPassword(cfg.Username, cfg.Password)
	}

	return proxyURL, nil
}

// buildSocks5Dialer creates a SOCKS5 proxy dialer.
func buildSocks5Dialer(cfg *ProxyConfig) (proxy.Dialer, error) {
	addr := cfg.Addr
	if strings.Contains(addr, "://") {
		// Strip socks5:// prefix if present
		if u, err := url.Parse(addr); err == nil {
			addr = u.Host
		}
	}

	var auth *proxy.Auth
	if cfg.Username != "" {
		auth = &proxy.Auth{
			User:     cfg.Username,
			Password: cfg.Password,
		}
	}

	return proxy.SOCKS5("tcp", addr, auth, proxy.Direct)
}
