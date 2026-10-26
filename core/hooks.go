package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// HookEventType enumerates the lifecycle events that can trigger hooks.
type HookEventType string

const (
	HookEventMessageReceived    HookEventType = "message.received"
	HookEventMessageSent        HookEventType = "message.sent"
	HookEventSessionStarted     HookEventType = "session.started"
	HookEventSessionEnded       HookEventType = "session.ended"
	HookEventCronTriggered      HookEventType = "cron.triggered"
	HookEventTimerTriggered     HookEventType = "timer.triggered"
	HookEventPermissionRequested HookEventType = "permission.requested"
	HookEventError              HookEventType = "error"
)

// HookHandlerType is the execution strategy for a hook.
type HookHandlerType string

const (
	HookHandlerCommand HookHandlerType = "command"
	HookHandlerHTTP    HookHandlerType = "http"
)

// HookConfig is the user-facing configuration for a single hook rule.
type HookConfig struct {
	Event   string `toml:"event" json:"event"`
	Type    string `toml:"type" json:"type"`       // "command" or "http"
	Command string `toml:"command" json:"command,omitempty"`
	URL     string `toml:"url" json:"url,omitempty"`
	Timeout int    `toml:"timeout" json:"timeout,omitempty"` // seconds; 0 = default (10s cmd, 5s http)
	Async   *bool  `toml:"async" json:"async,omitempty"`     // nil = true (async by default)
}

func (h *HookConfig) isAsync() bool {
	return h.Async == nil || *h.Async
}

func (h *HookConfig) timeoutDuration() time.Duration {
	if h.Timeout > 0 {
		return time.Duration(h.Timeout) * time.Second
	}
	if h.Type == "http" {
		return 5 * time.Second
	}
	return 10 * time.Second
}

// HookEvent is the payload delivered to hook handlers.
type HookEvent struct {
	Event      HookEventType  `json:"event"`
	Timestamp  time.Time      `json:"timestamp"`
	Project    string         `json:"project"`
	SessionKey string         `json:"session_key,omitempty"`
	Platform   string         `json:"platform,omitempty"`
	UserID     string         `json:"user_id,omitempty"`
	UserName   string         `json:"user_name,omitempty"`
	Content    string         `json:"content,omitempty"`
	Error      string         `json:"error,omitempty"`
	Extra      map[string]any `json:"extra,omitempty"`
}

// HookManager dispatches lifecycle events to configured hook handlers.
type HookManager struct {
	hooks       []HookConfig
	project     string
	shell       string // shell binary (e.g. "sh", "/bin/zsh")
	shellFlag   string // shell flag (e.g. "-c", "-Command")
	shellProfile string // prepended to every command
	mu          sync.RWMutex
	client      *http.Client
}

// NewHookManager creates a manager for the given project name.
func NewHookManager(project string, hooks []HookConfig, shell, shellFlag, shellProfile string) *HookManager {
	valid := make([]HookConfig, 0, len(hooks))
	for _, h := range hooks {
		if err := validateHookConfig(h); err != nil {
			slog.Warn("hooks: skipping invalid config", "project", project, "error", err)
			continue
		}
		valid = append(valid, h)
	}
	return &HookManager{
		hooks:       valid,
		project:     project,
		shell:       shell,
		shellFlag:   shellFlag,
		shellProfile: shellProfile,
		client: &http.Client{
			Transport: &http.Transport{
				MaxIdleConns:        100,
				IdleConnTimeout:     90 * time.Second,
				MaxIdleConnsPerHost: 10,
			},
		},
	}
}

func validateHookConfig(h HookConfig) error {
	if h.Event == "" {
		return fmt.Errorf("event is required")
	}
	switch HookHandlerType(h.Type) {
	case HookHandlerCommand:
		if h.Command == "" {
			return fmt.Errorf("command is required for type=command")
		}
	case HookHandlerHTTP:
		if h.URL == "" {
			return fmt.Errorf("url is required for type=http")
		}
		if !strings.HasPrefix(h.URL, "http://") && !strings.HasPrefix(h.URL, "https://") {
			return fmt.Errorf("url must start with http:// or https://")
		}
	default:
		return fmt.Errorf("unknown handler type %q (must be command or http)", h.Type)
	}
	return nil
}

// Emit dispatches an event to all matching hooks.
func (hm *HookManager) Emit(event HookEvent) {
	if hm == nil {
		return
	}
	event.Project = hm.project
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	hm.mu.RLock()
	hooks := hm.hooks
	hm.mu.RUnlock()

	for i := range hooks {
		h := &hooks[i]
		if !matchEvent(h.Event, string(event.Event)) {
			continue
		}
		if h.isAsync() {
			go hm.execute(h, event)
		} else {
			hm.execute(h, event)
		}
	}
}

// matchEvent checks if a hook's event pattern matches the fired event.
// Supports exact match and wildcard "*".
func matchEvent(pattern, event string) bool {
	if pattern == "*" {
		return true
	}
	return strings.EqualFold(pattern, event)
}

func (hm *HookManager) execute(h *HookConfig, event HookEvent) {
	switch HookHandlerType(h.Type) {
	case HookHandlerCommand:
		hm.executeCommand(h, event)
	case HookHandlerHTTP:
		hm.executeHTTP(h, event)
	}
}

func (hm *HookManager) executeCommand(h *HookConfig, event HookEvent) {
	timeout := h.timeoutDuration()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := shellExecCommand(ctx, hm.shell, hm.shellFlag, hm.shellProfile, h.Command)
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = append(os.Environ(), eventToEnv(event)...)

	output, err := cmd.CombinedOutput()
	if err != nil {
		slog.Warn("hooks: command failed",
			"project", hm.project, "event", event.Event,
			"command", truncateStr(h.Command, 80),
			"error", err,
			"output", truncateStr(strings.TrimSpace(string(output)), 500),
		)
		return
	}
	slog.Debug("hooks: command executed",
		"project", hm.project, "event", event.Event,
		"command", truncateStr(h.Command, 80),
	)
}

func (hm *HookManager) executeHTTP(h *HookConfig, event HookEvent) {
	body, err := json.Marshal(event)
	if err != nil {
		slog.Warn("hooks: marshal event failed", "error", err)
		return
	}

	var lastErr error
	const maxRetries = 1
	const retryDelay = 500 * time.Millisecond

	// One deadline for the whole operation, not one per attempt. A
	// synchronous hook blocks the caller that emitted the event
	// (Emit runs non-async hooks inline), so `timeout` has to bound the
	// total time spent here — giving each attempt its own fresh timeout
	// would let a sync hook block for 2*timeout + retryDelay, twice what
	// the operator configured. Retrying still happens whenever the budget
	// survives the first attempt, which is the common transient-failure
	// case; if the first attempt exhausts the deadline there is simply no
	// time left to retry, which is the correct outcome.
	ctx, cancel := context.WithTimeout(context.Background(), h.timeoutDuration())
	defer cancel()

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			slog.Info("hooks: http retry",
				"project", hm.project, "event", event.Event,
				"url", h.URL, "attempt", attempt,
			)
			// Respect the deadline while backing off, so a nearly
			// exhausted budget does not add a pointless sleep.
			select {
			case <-time.After(retryDelay):
			case <-ctx.Done():
			}
		}
		if ctx.Err() != nil {
			lastErr = ctx.Err()
			break
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(body))
		if err != nil {
			lastErr = err
			slog.Warn("hooks: create request failed", "url", h.URL, "error", err)
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "CC-Connect-Hooks/1.0")
		req.Header.Set("X-Hook-Event", string(event.Event))

		resp, err := hm.client.Do(req)
		if err != nil {
			lastErr = err
			slog.Warn("hooks: http request failed",
				"project", hm.project, "event", event.Event,
				"url", h.URL, "error", err,
			)
			continue
		}
		if closeErr := resp.Body.Close(); closeErr != nil {
			slog.Debug("hooks: resp.Body.Close()", "error", closeErr)
		}

		if resp.StatusCode >= 400 {
			lastErr = fmt.Errorf("http %d", resp.StatusCode)
			// Retry only failures that can plausibly succeed on a second
			// attempt: transport errors (handled above) and 5xx, which are
			// server-side. A 4xx is the endpoint telling us the request
			// itself is wrong — 400/401/403/404/410 will return the same
			// thing every time, so retrying just delays the log line by
			// retryDelay. 429 is the exception: it explicitly asks for a
			// later retry.
			if resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
				slog.Warn("hooks: http response error",
					"project", hm.project, "event", event.Event,
					"url", h.URL, "status", resp.StatusCode,
				)
				return
			}
			slog.Warn("hooks: http response error, retrying",
				"project", hm.project, "event", event.Event,
				"url", h.URL, "status", resp.StatusCode,
			)
			continue
		}
		slog.Debug("hooks: http delivered",
			"project", hm.project, "event", event.Event,
			"url", h.URL, "status", resp.StatusCode,
		)
		return
	}
	if lastErr != nil {
		slog.Warn("hooks: http failed after retries",
			"project", hm.project, "event", event.Event,
			"url", h.URL, "error", lastErr,
		)
	}
}

// eventToEnv converts a HookEvent to environment variables for shell hooks.
func eventToEnv(e HookEvent) []string {
	env := []string{
		"CC_HOOK_EVENT=" + string(e.Event),
		"CC_HOOK_PROJECT=" + e.Project,
		"CC_HOOK_TIMESTAMP=" + e.Timestamp.Format(time.RFC3339),
	}
	if e.SessionKey != "" {
		env = append(env, "CC_HOOK_SESSION_KEY="+e.SessionKey)
	}
	if e.Platform != "" {
		env = append(env, "CC_HOOK_PLATFORM="+e.Platform)
	}
	if e.UserID != "" {
		env = append(env, "CC_HOOK_USER_ID="+e.UserID)
	}
	if e.UserName != "" {
		env = append(env, "CC_HOOK_USER_NAME="+e.UserName)
	}
	if e.Content != "" {
		env = append(env, "CC_HOOK_CONTENT="+e.Content)
	}
	if e.Error != "" {
		env = append(env, "CC_HOOK_ERROR="+e.Error)
	}
	return env
}

// Hooks returns the current hook configurations (for management API / testing).
func (hm *HookManager) Hooks() []HookConfig {
	if hm == nil {
		return nil
	}
	hm.mu.RLock()
	defer hm.mu.RUnlock()
	out := make([]HookConfig, len(hm.hooks))
	copy(out, hm.hooks)
	return out
}
