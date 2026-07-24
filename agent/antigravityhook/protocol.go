package antigravityhook

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

const (
	EnvAddress = "CC_CONNECT_AGY_PERMISSION_ADDR"
	EnvToken   = "CC_CONNECT_AGY_PERMISSION_TOKEN"

	maxHookInput          = 4 << 20
	bridgeDialTimeout     = 5 * time.Second
	bridgeResponseTimeout = 24 * time.Hour
)

type BridgeRequest struct {
	Token     string          `json:"token"`
	HookInput json.RawMessage `json:"hook_input"`
}

type BridgeResponse struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
}

// Relay forwards one Agy hook invocation to the owning cc-connect session.
func Relay(in io.Reader, out io.Writer, address, token string) error {
	if strings.TrimSpace(address) == "" || strings.TrimSpace(token) == "" {
		return fmt.Errorf("permission bridge environment is missing")
	}

	input, err := io.ReadAll(io.LimitReader(in, maxHookInput+1))
	if err != nil {
		return fmt.Errorf("read hook input: %w", err)
	}
	if len(input) > maxHookInput {
		return fmt.Errorf("hook input exceeds %d bytes", maxHookInput)
	}
	if !json.Valid(input) {
		return fmt.Errorf("hook input is not valid JSON")
	}

	conn, err := net.DialTimeout("tcp", address, bridgeDialTimeout)
	if err != nil {
		return fmt.Errorf("connect permission bridge: %w", err)
	}
	defer func() { _ = conn.Close() }()
	// The listener is started before agy runs this hook, so dial failures should
	// fail closed quickly. After connect, wait much longer for a human response.
	_ = conn.SetDeadline(time.Now().Add(bridgeResponseTimeout))

	if err := json.NewEncoder(conn).Encode(BridgeRequest{Token: token, HookInput: input}); err != nil {
		return fmt.Errorf("send permission request: %w", err)
	}

	var response BridgeResponse
	if err := json.NewDecoder(io.LimitReader(conn, 64<<10)).Decode(&response); err != nil {
		return fmt.Errorf("read permission response: %w", err)
	}
	switch response.Decision {
	case "allow", "deny":
	default:
		return fmt.Errorf("invalid permission decision %q", response.Decision)
	}

	if err := json.NewEncoder(out).Encode(response); err != nil {
		return fmt.Errorf("write hook response: %w", err)
	}
	return nil
}
