package silk

import (
	"fmt"
	"net/url"
	"strings"
)

// normalizeWebSocketURL maps HTTP(S) server addresses to WebSocket schemes.
// https:// and wss:// use TLS; http:// and ws:// are plain.
// Bare host:port defaults to ws://.
func normalizeWebSocketURL(server string) (string, error) {
	raw := strings.TrimSpace(server)
	if raw == "" {
		return "", nil
	}

	lower := strings.ToLower(raw)
	var wsScheme string
	var rest string

	switch {
	case strings.HasPrefix(lower, "wss://"):
		wsScheme, rest = "wss", raw[6:]
	case strings.HasPrefix(lower, "https://"):
		wsScheme, rest = "wss", raw[8:]
	case strings.HasPrefix(lower, "ws://"):
		wsScheme, rest = "ws", raw[5:]
	case strings.HasPrefix(lower, "http://"):
		wsScheme, rest = "ws", raw[7:]
	default:
		wsScheme, rest = "ws", raw
	}

	rest = strings.TrimRight(rest, "/")
	u, err := url.Parse(wsScheme + "://" + rest)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", fmt.Errorf("silk: missing host in server URL")
	}
	return u.String(), nil
}

func parseBoolOpt(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "1", "true", "yes", "on":
			return true
		}
	}
	return false
}
