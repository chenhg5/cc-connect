// Package grokbot bridges cc-connect to Grok Bot, the desktop/cloud assistant
// woken by a Cursor automation webhook.
//
// A turn does not run a local CLI. Send posts the user message to the
// configured webhook, then waits until Grok Bot writes the reply back — either
// HTTP POST /outbox/<id> on the local callback listener, or a JSON file in the
// outbox directory. The webhook key is read from the environment and is never
// written to logs.
package grokbot

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterAgent("grokbot", New)
}

const (
	defaultKeyEnv      = "GROK_BOT_WEBHOOK_KEY"
	defaultCallback    = "127.0.0.1:9876"
	defaultTimeout     = 6 * time.Minute
	defaultMaxEmbed    = 2 << 20
	webhookHTTPTimeout = 30 * time.Second
	maxReplyBytes      = 32 << 20
	maxMediaBytes      = 50 << 20
)

// Agent is one configured Grok Bot backend. Sessions share its callback
// listener and outbox directory.
type Agent struct {
	mu sync.Mutex

	webhookURL string
	apiKey     string // never log
	authHeader string // never log
	keyEnv     string

	workDir string

	timeout time.Duration

	callbackEnabled       bool
	callbackAuth          bool
	callbackInsecureOK    bool
	callbackAddr          string
	callbackAdvertiseHost string
	callbackBase          string
	replyToken            string // never log

	outboxEnabled  bool
	explicitOutbox string
	pathAllow      []string

	embedImages   bool
	maxEmbedBytes int

	client *http.Client

	srv *http.Server

	waiters map[string]chan replyMsg
	done    map[string]time.Time
	closed  bool
}

// New builds a Grok Bot agent. webhook_url is required. The bearer key is
// read from the environment variable named by webhook_key_env (default
// GROK_BOT_WEBHOOK_KEY) and must not appear in config.
func New(opts map[string]any) (core.Agent, error) {
	cfg, err := parseOptions(opts)
	if err != nil {
		return nil, err
	}
	a := &Agent{
		webhookURL:            cfg.webhookURL,
		apiKey:                cfg.apiKey,
		authHeader:            cfg.authHeader,
		keyEnv:                cfg.keyEnv,
		workDir:               cfg.workDir,
		timeout:               cfg.timeout,
		callbackEnabled:       cfg.callbackEnabled,
		callbackAuth:          cfg.callbackAuth,
		callbackInsecureOK:    cfg.callbackInsecureOK,
		callbackAddr:          cfg.callbackAddr,
		callbackAdvertiseHost: cfg.callbackAdvertiseHost,
		outboxEnabled:         cfg.outboxEnabled,
		explicitOutbox:        cfg.explicitOutbox,
		pathAllow:             cfg.pathAllow,
		embedImages:           cfg.embedImages,
		maxEmbedBytes:         cfg.maxEmbedBytes,
		client:                &http.Client{Timeout: webhookHTTPTimeout},
		waiters:               make(map[string]chan replyMsg),
		done:                  make(map[string]time.Time),
	}
	if cfg.callbackAuth {
		token, err := randomHex(32)
		if err != nil {
			return nil, fmt.Errorf("grokbot: reply token: %w", err)
		}
		a.replyToken = token
	}
	if a.callbackEnabled {
		if err := a.listen(); err != nil {
			return nil, err
		}
	}
	host := cfg.webhookHost
	slog.Info("grokbot: agent created",
		"webhook_host", host,
		"callback", a.callbackBase,
		"outbox", a.outboxEnabled,
		"timeout", a.timeout.String(),
		"embed_images", a.embedImages,
	)
	return a, nil
}

func (a *Agent) Name() string { return "grokbot" }

func (a *Agent) StartSession(ctx context.Context, sessionID string) (core.AgentSession, error) {
	a.mu.Lock()
	closed := a.closed
	a.mu.Unlock()
	if closed {
		return nil, fmt.Errorf("grokbot: agent is stopped")
	}
	return newSession(ctx, a, sessionID), nil
}

func (a *Agent) ListSessions(context.Context) ([]core.AgentSessionInfo, error) {
	return nil, nil
}

func (a *Agent) Stop() error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	srv := a.srv
	for id, ch := range a.waiters {
		select {
		case ch <- replyMsg{err: fmt.Errorf("grokbot: agent stopped")}:
		default:
		}
		delete(a.waiters, id)
	}
	a.mu.Unlock()
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			slog.Warn("grokbot: callback shutdown", "error", err)
		}
	}
	return nil
}

func (a *Agent) SetWorkDir(dir string) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return
	}
	a.mu.Lock()
	a.workDir = dir
	a.mu.Unlock()
	slog.Info("grokbot: work_dir changed", "work_dir", dir)
}

func (a *Agent) GetWorkDir() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.workDir
}

func (a *Agent) CLIBinaryName() string  { return "" }
func (a *Agent) CLIDisplayName() string { return "Grok Bot" }

func (a *Agent) DoctorChecks(context.Context) []core.DoctorCheckResult {
	a.mu.Lock()
	defer a.mu.Unlock()
	host := a.webhookURL
	if u, err := url.Parse(a.webhookURL); err == nil {
		host = u.Host
	}
	checks := []core.DoctorCheckResult{
		{Name: "Grok Bot webhook", Status: core.DoctorPass, Detail: "URL configured (" + host + ")"},
		{Name: "Grok Bot webhook key", Status: core.DoctorPass, Detail: "set via " + a.keyEnv},
	}
	if a.callbackEnabled {
		detail := a.callbackBase
		if detail == "" {
			detail = a.callbackAddr
		}
		if a.callbackAuth {
			detail += " (reply token required)"
		}
		checks = append(checks, core.DoctorCheckResult{
			Name: "Grok Bot callback", Status: core.DoctorPass, Detail: detail,
		})
	} else {
		checks = append(checks, core.DoctorCheckResult{
			Name: "Grok Bot callback", Status: core.DoctorWarn, Detail: "HTTP callback disabled",
		})
	}
	if a.outboxEnabled {
		checks = append(checks, core.DoctorCheckResult{
			Name: "Grok Bot outbox", Status: core.DoctorPass, Detail: a.outboxDirLocked(),
		})
	}
	return checks
}

func (a *Agent) outboxDir() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.outboxDirLocked()
}

func (a *Agent) outboxDirLocked() string {
	if !a.outboxEnabled {
		return ""
	}
	if a.explicitOutbox != "" {
		return a.explicitOutbox
	}
	return filepath.Join(a.workDir, ".cc-connect", "grokbot", "outbox")
}

func (a *Agent) redact(s string) string {
	s = core.RedactToken(s, a.apiKey)
	s = core.RedactToken(s, a.authHeader)
	s = core.RedactToken(s, a.replyToken)
	return s
}

func (a *Agent) authorized(r *http.Request) bool {
	a.mu.Lock()
	required := a.callbackAuth
	token := a.replyToken
	a.mu.Unlock()
	if !required {
		return true
	}
	got := bearerToken(r.Header.Get("Authorization"))
	if got == "" {
		got = strings.TrimSpace(r.Header.Get("X-Grokbot-Token"))
	}
	if len(got) != len(token) || token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

type parsedConfig struct {
	webhookURL            string
	webhookHost           string
	apiKey                string
	authHeader            string
	keyEnv                string
	workDir               string
	timeout               time.Duration
	callbackEnabled       bool
	callbackAuth          bool
	callbackAddr          string
	callbackAdvertiseHost string
	callbackInsecureOK    bool
	webhookInsecureOK     bool
	outboxEnabled         bool
	explicitOutbox        string
	embedImages           bool
	maxEmbedBytes         int
	pathAllow             []string
}

func parseOptions(opts map[string]any) (parsedConfig, error) {
	if opts == nil {
		opts = map[string]any{}
	}
	var cfg parsedConfig
	cfg.keyEnv = defaultKeyEnv
	if s, _ := opts["webhook_key_env"].(string); strings.TrimSpace(s) != "" {
		cfg.keyEnv = strings.TrimSpace(s)
	}
	if s, ok := opts["webhook_key"].(string); ok && strings.TrimSpace(s) != "" {
		return parsedConfig{}, fmt.Errorf("grokbot: do not put the webhook key in config; set environment variable %s", cfg.keyEnv)
	}

	rawURL, _ := opts["webhook_url"].(string)
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return parsedConfig{}, fmt.Errorf("grokbot: webhook_url is required")
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return parsedConfig{}, fmt.Errorf("grokbot: webhook_url must be an http(s) URL")
	}
	if v, ok := boolFromAny(opts["webhook_insecure_ok"]); ok {
		cfg.webhookInsecureOK = v
	}
	if u.Scheme == "http" && !isLoopbackBindHost(u.Hostname()) && !cfg.webhookInsecureOK {
		return parsedConfig{}, fmt.Errorf("grokbot: webhook_url host %s is not https; the API key and reply token would be sent in cleartext. Use https, loopback http for a local mock, or webhook_insecure_ok = true on a trusted network", u.Hostname())
	}
	if u.Scheme == "http" && !isLoopbackBindHost(u.Hostname()) {
		slog.Warn("grokbot: webhook_url is not https; the key and reply token are sent in cleartext", "host", u.Hostname())
	}
	cfg.webhookURL = rawURL
	cfg.webhookHost = u.Host

	rawKey := strings.TrimSpace(os.Getenv(cfg.keyEnv))
	if rawKey == "" {
		return parsedConfig{}, fmt.Errorf("grokbot: webhook key is empty; set environment variable %s", cfg.keyEnv)
	}
	cfg.apiKey = rawKey
	cfg.authHeader = rawKey
	if !strings.HasPrefix(strings.ToLower(rawKey), "bearer ") {
		cfg.apiKey = rawKey
		cfg.authHeader = "Bearer " + rawKey
	} else {
		cfg.apiKey = strings.TrimSpace(rawKey[len("bearer "):])
		cfg.authHeader = rawKey
	}

	cfg.workDir = "."
	if s, _ := opts["work_dir"].(string); strings.TrimSpace(s) != "" {
		cfg.workDir = expandHome(strings.TrimSpace(s))
	}
	timeout, err := parseTimeout(opts)
	if err != nil {
		return parsedConfig{}, err
	}
	cfg.timeout = timeout

	cfg.callbackEnabled = true
	if v, ok := boolFromAny(opts["callback"]); ok {
		cfg.callbackEnabled = v
	}
	cfg.callbackAuth = true
	if v, ok := boolFromAny(opts["callback_auth"]); ok {
		cfg.callbackAuth = v
	}
	if v, ok := boolFromAny(opts["callback_insecure_ok"]); ok {
		cfg.callbackInsecureOK = v
	}
	cfg.callbackAddr = defaultCallback
	if s, _ := opts["callback_addr"].(string); strings.TrimSpace(s) != "" {
		cfg.callbackAddr = strings.TrimSpace(s)
	}
	if cfg.callbackEnabled {
		host, _, err := net.SplitHostPort(cfg.callbackAddr)
		if err != nil {
			return parsedConfig{}, fmt.Errorf("grokbot: callback_addr: %w", err)
		}
		if !cfg.callbackAuth && !isLoopbackBindHost(host) && !cfg.callbackInsecureOK {
			return parsedConfig{}, fmt.Errorf("grokbot: callback_auth=false cannot listen on %s; use a loopback callback_addr or set callback_insecure_ok = true", cfg.callbackAddr)
		}
		if isWildcardBindHost(host) {
			adv, err := normalizeAdvertiseHost(stringOpt(opts, "callback_advertise_host"))
			if err != nil {
				return parsedConfig{}, err
			}
			if adv == "" {
				return parsedConfig{}, fmt.Errorf("grokbot: callback_addr %s listens on every interface; set callback_advertise_host to the host or IP Grok Bot can reach. A loopback callback_addr does not need it", cfg.callbackAddr)
			}
			cfg.callbackAdvertiseHost = adv
		}
	}
	if adv, err := normalizeAdvertiseHost(stringOpt(opts, "callback_advertise_host")); err != nil {
		return parsedConfig{}, err
	} else if adv != "" {
		cfg.callbackAdvertiseHost = adv
	}
	if v, ok := opts["reply_path_allow"]; ok {
		list, err := stringList(v)
		if err != nil {
			return parsedConfig{}, err
		}
		for _, dir := range list {
			dir = expandHome(strings.TrimSpace(dir))
			if dir != "" {
				cfg.pathAllow = append(cfg.pathAllow, dir)
			}
		}
	}

	cfg.outboxEnabled = true
	if v, ok := boolFromAny(opts["outbox"]); ok {
		cfg.outboxEnabled = v
	}
	if s, _ := opts["outbox_dir"].(string); strings.TrimSpace(s) != "" {
		cfg.explicitOutbox = expandHome(strings.TrimSpace(s))
	}
	if !cfg.callbackEnabled && !cfg.outboxEnabled {
		return parsedConfig{}, fmt.Errorf("grokbot: enable callback or outbox so a reply can be delivered")
	}

	if v, ok := boolFromAny(opts["embed_images"]); ok {
		cfg.embedImages = v
	}
	cfg.maxEmbedBytes = defaultMaxEmbed
	if v, ok := opts["max_embed_bytes"]; ok {
		n, ok := intFromAny(v)
		if !ok || n < 0 {
			return parsedConfig{}, fmt.Errorf("grokbot: invalid max_embed_bytes")
		}
		cfg.maxEmbedBytes = n
	}
	return cfg, nil
}

func parseTimeout(opts map[string]any) (time.Duration, error) {
	if s, _ := opts["timeout"].(string); strings.TrimSpace(s) != "" {
		d, err := time.ParseDuration(strings.TrimSpace(s))
		if err != nil || d <= 0 {
			return 0, fmt.Errorf("grokbot: invalid timeout %q (use a Go duration such as 6m or 180s)", strings.TrimSpace(s))
		}
		return d, nil
	}
	if v, ok := opts["timeout_mins"]; ok {
		n, ok := intFromAny(v)
		if !ok || n <= 0 {
			return 0, fmt.Errorf("grokbot: timeout_mins must be a positive number")
		}
		return time.Duration(n) * time.Minute, nil
	}
	return defaultTimeout, nil
}

func boolFromAny(v any) (bool, bool) {
	switch b := v.(type) {
	case bool:
		return b, true
	case string:
		switch strings.ToLower(strings.TrimSpace(b)) {
		case "1", "true", "yes", "on":
			return true, true
		case "0", "false", "no", "off":
			return false, true
		}
	}
	return false, false
}

func intFromAny(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(n))
		if err != nil {
			return 0, false
		}
		return i, true
	default:
		return 0, false
	}
}

func expandHome(path string) string {
	if path == "" || !strings.HasPrefix(path, "~") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		return filepath.Join(home, path[2:])
	}
	return path
}

// isLoopbackBindHost reports whether a callback bind stays on the local machine.
// Empty host, 0.0.0.0, and :: listen on every interface.
func isLoopbackBindHost(host string) bool {
	host = strings.Trim(host, "[]")
	if isWildcardBindHost(host) {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isWildcardBindHost(host string) bool {
	host = strings.Trim(host, "[]")
	return host == "" || host == "0.0.0.0" || host == "::"
}

// normalizeAdvertiseHost accepts a hostname, IP, or host:port. A wildcard is
// rejected because it is not a destination the routine can dial.
func normalizeAdvertiseHost(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil
	}
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil || u.Host == "" {
			return "", fmt.Errorf("grokbot: callback_advertise_host must be a host or IP")
		}
		s = u.Hostname()
	} else if h, _, err := net.SplitHostPort(s); err == nil {
		s = h
	}
	s = strings.Trim(s, "[]")
	if s == "" || isWildcardBindHost(s) {
		return "", fmt.Errorf("grokbot: callback_advertise_host must be an address the routine can connect to, not a wildcard")
	}
	return s, nil
}

func stringOpt(opts map[string]any, key string) string {
	s, _ := opts[key].(string)
	return s
}

func stringList(v any) ([]string, error) {
	switch t := v.(type) {
	case string:
		if strings.TrimSpace(t) == "" {
			return nil, nil
		}
		var out []string
		out = append(out, strings.Split(t, ",")...)
		return out, nil
	case []string:
		return t, nil
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("grokbot: reply_path_allow entries must be strings")
			}
			out = append(out, s)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("grokbot: reply_path_allow must be a list of directories")
	}
}

func bearerToken(header string) string {
	header = strings.TrimSpace(header)
	if len(header) >= 7 && strings.EqualFold(header[:7], "bearer ") {
		return strings.TrimSpace(header[7:])
	}
	return header
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func newID() string {
	id, err := randomHex(8)
	if err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return id
}

var (
	_ core.Agent           = (*Agent)(nil)
	_ core.WorkDirSwitcher = (*Agent)(nil)
	_ core.AgentDoctorInfo = (*Agent)(nil)
	_ core.DoctorChecker   = (*Agent)(nil)
)
