package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ProviderProxy is a lightweight local reverse proxy that rewrites
// incompatible Anthropic API fields for third-party providers.
//
// Some providers (e.g. SiliconFlow) don't support thinking.type "adaptive"
// sent by Claude Code 2.x. The proxy rewrites the thinking field to
// the configured override value before forwarding.
type ProviderProxy struct {
	targetURL        string
	thinkingOverride string
	listener         net.Listener
	server           *http.Server
	once             sync.Once
}

// NewProviderProxy creates and starts a local reverse proxy for the
// given upstream URL. thinkingOverride controls what thinking.type to
// rewrite "adaptive" to (e.g. "disabled" or "enabled").
// Returns the local URL to use as ANTHROPIC_BASE_URL.
func NewProviderProxy(targetURL, thinkingOverride string) (*ProviderProxy, string, error) {
	target, err := url.Parse(strings.TrimRight(targetURL, "/"))
	if err != nil {
		return nil, "", fmt.Errorf("providerproxy: parse target: %w", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", fmt.Errorf("providerproxy: listen: %w", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	origDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		origDirector(req)
		req.Host = target.Host
	}
	proxy.FlushInterval = -1 // flush SSE events immediately

	override := thinkingOverride
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/messages") {
			rewriteThinkingInRequest(r, override)
		}
		proxy.ServeHTTP(w, r)
	})

	pp := &ProviderProxy{
		targetURL:        targetURL,
		thinkingOverride: thinkingOverride,
		listener:         listener,
		server: &http.Server{
			Handler:      mux,
			ReadTimeout:  10 * time.Minute,
			WriteTimeout: 10 * time.Minute,
		},
	}

	go func() {
		if err := pp.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			slog.Error("providerproxy: serve error", "error", err)
		}
	}()

	localURL := fmt.Sprintf("http://127.0.0.1:%d", listener.Addr().(*net.TCPAddr).Port)
	slog.Info("providerproxy: started", "target", targetURL, "local", localURL, "thinking", thinkingOverride)
	return pp, localURL, nil
}

// Close shuts down the proxy.
func (pp *ProviderProxy) Close() {
	pp.once.Do(func() {
		pp.server.Close()
	})
}

// rewriteThinkingInRequest reads the request body and rewrites
// thinking.type "adaptive" to the given override value.
func rewriteThinkingInRequest(r *http.Request, override string) {
	if r.Body == nil {
		return
	}
	body, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		r.Body = io.NopCloser(bytes.NewReader(body))
		return
	}

	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		return
	}

	modified := false
	if thinking, ok := data["thinking"].(map[string]any); ok {
		if t, ok := thinking["type"].(string); ok && t == "adaptive" {
			thinking["type"] = override
			if override == "disabled" {
				delete(thinking, "budget_tokens")
			}
			modified = true
			slog.Debug("providerproxy: rewrote thinking adaptive →", "override", override)
		}
	}

	if meta, ok := data["metadata"].(map[string]any); ok {
		if uid, ok := meta["user_id"].(string); ok && uid != "" {
			if sanitized := sanitizeAnthropicUserID(uid); sanitized != uid {
				slog.Warn("providerproxy: sanitized metadata.user_id to satisfy ^[a-zA-Z0-9_-]+$",
					"original_len", len(uid), "sanitized", sanitized)
				meta["user_id"] = sanitized
				modified = true
			}
		}
	}

	if !modified {
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		return
	}

	newBody, err := json.Marshal(data)
	if err != nil {
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(newBody))
	r.ContentLength = int64(len(newBody))
}

// maxAnthropicUserIDLen is the Anthropic API's documented length cap for
// metadata.user_id. Compatible gateways (e.g. DeepSeek) enforce the same
// cap. See https://docs.anthropic.com/en/api/messages.
const maxAnthropicUserIDLen = 256

// sanitizeAnthropicUserID ensures the value matches ^[a-zA-Z0-9_-]+$ and
// is no longer than maxAnthropicUserIDLen bytes, per the Anthropic API
// spec and the matching constraint enforced by DeepSeek and other
// Anthropic-compatible gateways (#864).
//
// If the input is already compliant, it is returned unchanged. Otherwise
// the entire string is replaced with a deterministic, collision-resistant
// hash digest prefixed with "h_" so operators can grep logs and re-derive
// the original by sha256-hashing the same input. The hash form is always
// 34 bytes long, well under the 256-byte cap.
//
// We do not split-and-keep-ASCII-runs: keeping fragments of the original
// user_id would leak partial PII (e.g. "user_" prefix from a Feishu
// open_id like "ou_张三") and still require per-provider acceptance of
// mixed-script strings. A full-string hash is the simplest, safest
// transform that satisfies the constraint and is identical across runs.
func sanitizeAnthropicUserID(s string) string {
	if s == "" {
		return s
	}
	if len(s) <= maxAnthropicUserIDLen && isAnthropicUserIDCompliant(s) {
		return s
	}
	sum := sha256.Sum256([]byte(s))
	return "h_" + hex.EncodeToString(sum[:16])
}

// isAnthropicUserIDCompliant reports whether s is non-empty, no longer
// than maxAnthropicUserIDLen bytes, and consists solely of the
// characters allowed by the Anthropic API's metadata.user_id pattern
// ^[a-zA-Z0-9_-]+$.
func isAnthropicUserIDCompliant(s string) bool {
	if s == "" || len(s) > maxAnthropicUserIDLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') &&
			(c < 'A' || c > 'Z') &&
			(c < '0' || c > '9') &&
			c != '_' && c != '-' {
			return false
		}
	}
	return true
}
