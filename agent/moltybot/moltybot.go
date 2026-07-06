// Package moltybot forwards cc-connect platform messages to a local MoltyBot
// bridge. It does not spawn or drive a coding-agent process.
package moltybot

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const (
	agentName                    = "moltybot"
	defaultSessionMode           = "per_remote_user"
	sessionModePerRemoteUser     = "per_remote_user"
	sessionModePassthrough       = "passthrough"
	defaultBridgeRequestTimeout  = 2 * time.Minute
	bridgeMessagesEndpointSuffix = "/v1/messages"
)

func init() {
	core.RegisterAgent(agentName, New)
}

// Agent forwards turns to MoltyBot's local HTTP bridge.
type Agent struct {
	baseURL     string
	token       string
	sessionMode string
	client      *http.Client
}

// New creates a MoltyBot bridge agent.
//
// Required option:
//   - base_url: local MoltyBot bridge base URL, for example http://127.0.0.1:48999
//
// Optional options:
//   - token: bearer token sent to the bridge
//   - session_mode: per_remote_user (default) or passthrough
func New(opts map[string]any) (core.Agent, error) {
	baseURL, _ := opts["base_url"].(string)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("moltybot: base_url is required")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("moltybot: invalid base_url %q: %w", baseURL, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("moltybot: invalid base_url %q: missing scheme or host", baseURL)
	}

	token, _ := opts["token"].(string)
	sessionMode, err := parseSessionMode(opts["session_mode"])
	if err != nil {
		return nil, err
	}

	slog.Info(
		"moltybot: agent created",
		"base_url", baseURL,
		"session_mode", sessionMode,
		"token_configured", strings.TrimSpace(token) != "",
	)
	return &Agent{
		baseURL:     baseURL,
		token:       strings.TrimSpace(token),
		sessionMode: sessionMode,
		client: &http.Client{
			Timeout: defaultBridgeRequestTimeout,
		},
	}, nil
}

func parseSessionMode(raw any) (string, error) {
	value, _ := raw.(string)
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return defaultSessionMode, nil
	}
	switch value {
	case sessionModePerRemoteUser, sessionModePassthrough:
		return value, nil
	default:
		return "", fmt.Errorf("moltybot: unsupported session_mode %q", value)
	}
}

func (a *Agent) Name() string { return agentName }

func (a *Agent) StartSession(ctx context.Context, sessionID string) (core.AgentSession, error) {
	slog.Info("moltybot: starting session", "session_id", sessionID, "session_mode", a.sessionMode)
	return newSession(ctx, a.client, a.baseURL, a.token, a.sessionMode, sessionID), nil
}

func (a *Agent) ListSessions(_ context.Context) ([]core.AgentSessionInfo, error) {
	return nil, nil
}

func (a *Agent) Stop() error { return nil }

var _ core.Agent = (*Agent)(nil)
