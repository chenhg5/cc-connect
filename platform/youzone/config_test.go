package youzone

import (
	"testing"
	"time"
)

func TestParseConfigDefaults(t *testing.T) {
	cfg, err := parseConfig(map[string]any{
		"robot_id":     "robot-1",
		"access_token": "token",
		"tenant_id":    "tenant",
	})
	if err != nil {
		t.Fatalf("parseConfig() error = %v", err)
	}
	if cfg.baseURL != defaultBaseURL {
		t.Fatalf("baseURL = %q, want %q", cfg.baseURL, defaultBaseURL)
	}
	if cfg.apiPrefix != defaultAPIPrefix {
		t.Fatalf("apiPrefix = %q, want %q", cfg.apiPrefix, defaultAPIPrefix)
	}
	if got := cfg.websocketProtocols; len(got) != 1 || got[0] != "xmpp" {
		t.Fatalf("websocketProtocols = %#v, want [xmpp]", got)
	}
	if cfg.heartbeatMode != heartbeatXMPPWhitespace {
		t.Fatalf("heartbeatMode = %q, want %q", cfg.heartbeatMode, heartbeatXMPPWhitespace)
	}
	if cfg.pingInterval != 25*time.Second {
		t.Fatalf("pingInterval = %v, want 25s", cfg.pingInterval)
	}
}

func TestParseConfigRequiresAuth(t *testing.T) {
	_, err := parseConfig(map[string]any{
		"robot_id":  "robot",
		"tenant_id": "tenant",
	})
	if err == nil {
		t.Fatal("parseConfig() error = nil, want missing access_token")
	}
}

func TestParseConfigParsesListsAndDurations(t *testing.T) {
	cfg, err := parseConfig(map[string]any{
		"robot_id":                     "robot",
		"access_token":                 "token",
		"tenant_id":                    "tenant",
		"websocket_protocols":          "xmpp, custom",
		"reconnect_delays":             "500ms,2s",
		"ping_interval":                "10s",
		"heartbeat_mode":               "ws-ping",
		"auto_create_robot":            true,
		"machine_code":                 "machine",
		"robot_explain":                "explain",
		"enable_token_header_fallback": true,
	})
	if err != nil {
		t.Fatalf("parseConfig() error = %v", err)
	}
	if got := cfg.websocketProtocols; len(got) != 2 || got[0] != "xmpp" || got[1] != "custom" {
		t.Fatalf("websocketProtocols = %#v", got)
	}
	if got := cfg.reconnectDelays; len(got) != 2 || got[0] != 500*time.Millisecond || got[1] != 2*time.Second {
		t.Fatalf("reconnectDelays = %#v", got)
	}
	if cfg.heartbeatMode != heartbeatWSPing {
		t.Fatalf("heartbeatMode = %q", cfg.heartbeatMode)
	}
	if !cfg.autoCreateRobot || cfg.machineCode != "machine" || cfg.robotExplain != "explain" {
		t.Fatalf("robot discovery config not parsed: %#v", cfg)
	}
	if !cfg.enableTokenHeaderFallback {
		t.Fatal("enableTokenHeaderFallback = false, want true")
	}
}
