package youzone

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

func parseConfig(opts map[string]any) (config, error) {
	cfg := config{
		baseURL:            defaultBaseURL,
		apiPrefix:          defaultAPIPrefix,
		robotExplain:       defaultRobotExplain,
		websocketProtocols: []string{"xmpp"},
		heartbeatMode:      heartbeatXMPPWhitespace,
		pingInterval:       defaultPingInterval,
		reconnectDelays:    []time.Duration{time.Second, 3 * time.Second, 10 * time.Second, 30 * time.Second},
		httpTimeout:        defaultHTTPTimeout,
	}
	if v := optString(opts, "base_url"); v != "" {
		cfg.baseURL = strings.TrimRight(v, "/")
	}
	if _, err := url.ParseRequestURI(cfg.baseURL); err != nil {
		return cfg, fmt.Errorf("youzone: base_url: %w", err)
	}
	if v := optString(opts, "api_prefix"); v != "" {
		cfg.apiPrefix = normalizePrefix(v)
	}
	cfg.robotID = optString(opts, "robot_id")
	cfg.accessToken = optString(opts, "access_token")
	cfg.tenantID = optString(opts, "tenant_id")
	cfg.machineCode = optString(opts, "machine_code")
	cfg.robotExplain = defaultString(optString(opts, "robot_explain"), defaultRobotExplain)
	cfg.allowFrom = optString(opts, "allow_from")
	cfg.autoCreateRobot = optBool(opts, "auto_create_robot")
	cfg.enableTokenHeaderFallback = optBool(opts, "enable_token_header_fallback")
	cfg.logInboundRaw = optBool(opts, "log_inbound_raw")
	if v := optString(opts, "websocket_protocols"); v != "" {
		cfg.websocketProtocols = splitCSV(v)
	}
	if len(cfg.websocketProtocols) == 0 {
		cfg.websocketProtocols = []string{"xmpp"}
	}
	if v := optString(opts, "heartbeat_mode"); v != "" {
		cfg.heartbeatMode = strings.ToLower(strings.TrimSpace(v))
	}
	if cfg.heartbeatMode != heartbeatXMPPWhitespace && cfg.heartbeatMode != heartbeatWSPing {
		return cfg, fmt.Errorf("youzone: heartbeat_mode must be %q or %q", heartbeatXMPPWhitespace, heartbeatWSPing)
	}
	if v := optString(opts, "ping_interval"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return cfg, fmt.Errorf("youzone: ping_interval: %w", err)
		}
		cfg.pingInterval = d
	}
	if cfg.pingInterval < 5*time.Second {
		cfg.pingInterval = 5 * time.Second
	}
	if v := optString(opts, "reconnect_delays"); v != "" {
		delays, err := parseDurationList(v)
		if err != nil {
			return cfg, fmt.Errorf("youzone: reconnect_delays: %w", err)
		}
		cfg.reconnectDelays = delays
	}
	if v := optString(opts, "http_timeout"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return cfg, fmt.Errorf("youzone: http_timeout: %w", err)
		}
		cfg.httpTimeout = d
	}
	if cfg.accessToken == "" {
		return cfg, fmt.Errorf("youzone: access_token is required")
	}
	if cfg.tenantID == "" {
		return cfg, fmt.Errorf("youzone: tenant_id is required")
	}
	if cfg.robotID == "" && cfg.machineCode == "" {
		return cfg, fmt.Errorf("youzone: robot_id is required unless machine_code is configured")
	}
	if cfg.autoCreateRobot && cfg.machineCode == "" {
		return cfg, fmt.Errorf("youzone: machine_code is required when auto_create_robot is true")
	}
	return cfg, nil
}

func optString(opts map[string]any, key string) string {
	v, _ := opts[key].(string)
	return strings.TrimSpace(v)
}

func optBool(opts map[string]any, key string) bool {
	v, _ := opts[key].(bool)
	return v
}

func defaultString(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return strings.TrimSpace(v)
}

func normalizePrefix(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || v == "/" {
		return ""
	}
	v = "/" + strings.Trim(v, "/")
	return v
}

func splitCSV(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func parseDurationList(v string) ([]time.Duration, error) {
	parts := splitCSV(v)
	delays := make([]time.Duration, 0, len(parts))
	for _, part := range parts {
		d, err := time.ParseDuration(part)
		if err != nil {
			return nil, err
		}
		if d < 0 {
			d = 0
		}
		delays = append(delays, d)
	}
	if len(delays) == 0 {
		return nil, fmt.Errorf("empty duration list")
	}
	return delays, nil
}
