package core

import (
	"net/http"
	"net/url"
	"os"
	"time"
)

// getProxyURL returns the proxy URL from environment or common Clash ports
func getProxyURL() *url.URL {
	// Check common proxy env vars first
	proxyURL := os.Getenv("HTTP_PROXY")
	if proxyURL == "" {
		proxyURL = os.Getenv("HTTPS_PROXY")
	}
	if proxyURL == "" {
		proxyURL = os.Getenv("ALL_PROXY")
	}
	// Fallback to common Clash Verge port
	if proxyURL == "" {
		proxyURL = "http://127.0.0.1:7890"
	}
	
	if proxyURL != "" {
		if u, err := url.Parse(proxyURL); err == nil {
			return u
		}
	}
	return nil
}

// HTTPClient is a shared HTTP client with a reasonable timeout for platform use.
var HTTPClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
	},
}

func init() {
	// Set default proxy for HTTPClient
	if proxyURL := getProxyURL(); proxyURL != nil {
		transport := &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		}
		HTTPClient.Transport = transport
	}
}
