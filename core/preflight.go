package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// PreflightEventType enumerates decision-capable gate events.
type PreflightEventType string

const (
	PreflightEventMessage PreflightEventType = "message.preflight"
)

// PreflightDecisionValue is the action returned by a preflight check.
type PreflightDecisionValue string

const (
	PreflightContinue PreflightDecisionValue = "continue"
	PreflightBlock    PreflightDecisionValue = "block"
	PreflightIgnore   PreflightDecisionValue = "ignore"
)

const maxPreflightResponseBytes = 64 * 1024

// PreflightCheckConfig is a decision-capable HTTP gate.
type PreflightCheckConfig struct {
	Event          string `toml:"event" json:"event,omitempty"` // default: message.preflight
	Type           string `toml:"type" json:"type"`             // "http"
	URL            string `toml:"url" json:"url,omitempty"`
	Timeout        int    `toml:"timeout" json:"timeout,omitempty"`   // seconds; 0 = 5s
	OnError        string `toml:"on_error" json:"on_error,omitempty"` // block, continue, ignore; default block
	IncludeContent bool   `toml:"include_content" json:"include_content,omitempty"`
}

func (c PreflightCheckConfig) eventName() string {
	if strings.TrimSpace(c.Event) == "" {
		return string(PreflightEventMessage)
	}
	return c.Event
}

func (c PreflightCheckConfig) timeoutDuration() time.Duration {
	if c.Timeout > 0 {
		return time.Duration(c.Timeout) * time.Second
	}
	return 5 * time.Second
}

func (c PreflightCheckConfig) onErrorDecision(err error) PreflightDecision {
	decision := PreflightBlock
	switch PreflightDecisionValue(strings.ToLower(strings.TrimSpace(c.OnError))) {
	case PreflightContinue:
		decision = PreflightContinue
	case PreflightIgnore:
		decision = PreflightIgnore
	case PreflightBlock, "":
		decision = PreflightBlock
	}
	return PreflightDecision{
		Decision: decision,
		Code:     "preflight_error",
		Reason:   err.Error(),
	}
}

// PreflightEvent is the payload sent to preflight checks.
type PreflightEvent struct {
	Event      PreflightEventType `json:"event"`
	Timestamp  time.Time          `json:"timestamp"`
	Project    string             `json:"project"`
	SessionKey string             `json:"session_key,omitempty"`
	Platform   string             `json:"platform,omitempty"`
	MessageID  string             `json:"message_id,omitempty"`
	ChannelID  string             `json:"channel_id,omitempty"`
	UserID     string             `json:"user_id,omitempty"`
	UserName   string             `json:"user_name,omitempty"`
	ChatName   string             `json:"chat_name,omitempty"`
	Content    string             `json:"content,omitempty"`
	Extra      map[string]any     `json:"extra,omitempty"`
}

// PreflightDecision is the normalized decision returned by preflight checks.
type PreflightDecision struct {
	Decision   PreflightDecisionValue `json:"decision"`
	Code       string                 `json:"code,omitempty"`
	Message    string                 `json:"message,omitempty"`
	Reason     string                 `json:"reason,omitempty"`
	RetryAfter int                    `json:"retry_after,omitempty"`
}

type preflightHTTPResponse struct {
	Decision   string `json:"decision"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	Reason     string `json:"reason"`
	RetryAfter int    `json:"retry_after"`
}

// PreflightManager evaluates configured preflight checks.
type PreflightManager struct {
	checks  []PreflightCheckConfig
	project string
	mu      sync.RWMutex
	client  *http.Client
}

// NewPreflightManager creates a manager for decision-capable preflight checks.
func NewPreflightManager(project string, checks []PreflightCheckConfig) *PreflightManager {
	valid := make([]PreflightCheckConfig, 0, len(checks))
	for _, check := range checks {
		if err := validatePreflightCheckConfig(check); err != nil {
			slog.Warn("preflight: skipping invalid config", "project", project, "error", err)
			continue
		}
		valid = append(valid, check)
	}
	return &PreflightManager{
		checks:  valid,
		project: project,
		client:  &http.Client{},
	}
}

func validatePreflightCheckConfig(c PreflightCheckConfig) error {
	if c.eventName() == "" {
		return fmt.Errorf("event is required")
	}
	if HookHandlerType(c.Type) != HookHandlerHTTP {
		return fmt.Errorf("unknown preflight type %q (must be http)", c.Type)
	}
	if c.URL == "" {
		return fmt.Errorf("url is required for type=http")
	}
	if !strings.HasPrefix(c.URL, "http://") && !strings.HasPrefix(c.URL, "https://") {
		return fmt.Errorf("url must start with http:// or https://")
	}
	switch PreflightDecisionValue(strings.ToLower(strings.TrimSpace(c.OnError))) {
	case "", PreflightBlock, PreflightContinue, PreflightIgnore:
		return nil
	default:
		return fmt.Errorf("on_error must be block, continue, or ignore")
	}
}

// Evaluate runs matching preflight checks. All checks must continue; the first
// block or ignore decision stops evaluation.
func (pm *PreflightManager) Evaluate(event PreflightEvent) PreflightDecision {
	if pm == nil {
		return PreflightDecision{Decision: PreflightContinue}
	}
	if event.Event == "" {
		event.Event = PreflightEventMessage
	}
	event.Project = pm.project
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	pm.mu.RLock()
	checks := pm.checks
	pm.mu.RUnlock()

	for i := range checks {
		check := &checks[i]
		if !matchEvent(check.eventName(), string(event.Event)) {
			continue
		}
		payload := event
		if !check.IncludeContent {
			payload.Content = ""
		}
		decision, err := pm.executeHTTP(check, payload)
		if err != nil {
			decision = check.onErrorDecision(err)
		}
		if decision.Decision == "" {
			decision.Decision = PreflightContinue
		}
		switch decision.Decision {
		case PreflightContinue:
			continue
		case PreflightBlock, PreflightIgnore:
			return decision
		default:
			err := fmt.Errorf("invalid decision %q", decision.Decision)
			return check.onErrorDecision(err)
		}
	}
	return PreflightDecision{Decision: PreflightContinue}
}

func (pm *PreflightManager) executeHTTP(check *PreflightCheckConfig, event PreflightEvent) (PreflightDecision, error) {
	body, err := json.Marshal(event)
	if err != nil {
		return PreflightDecision{}, fmt.Errorf("marshal event: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), check.timeoutDuration())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, check.URL, bytes.NewReader(body))
	if err != nil {
		return PreflightDecision{}, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "CC-Connect-Preflight/1.0")
	req.Header.Set("X-Preflight-Event", string(event.Event))

	resp, err := pm.client.Do(req)
	if err != nil {
		return PreflightDecision{}, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return PreflightDecision{}, fmt.Errorf("http status %d", resp.StatusCode)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxPreflightResponseBytes))
	if err != nil {
		return PreflightDecision{}, fmt.Errorf("read response: %w", err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return PreflightDecision{Decision: PreflightContinue}, nil
	}

	var parsed preflightHTTPResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return PreflightDecision{}, fmt.Errorf("parse response: %w", err)
	}
	return PreflightDecision{
		Decision:   PreflightDecisionValue(strings.ToLower(strings.TrimSpace(parsed.Decision))),
		Code:       parsed.Code,
		Message:    parsed.Message,
		Reason:     parsed.Reason,
		RetryAfter: parsed.RetryAfter,
	}, nil
}

// Checks returns the current preflight check configurations.
func (pm *PreflightManager) Checks() []PreflightCheckConfig {
	if pm == nil {
		return nil
	}
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	out := make([]PreflightCheckConfig, len(pm.checks))
	copy(out, pm.checks)
	return out
}
