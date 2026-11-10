package core

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// defaultLoopbackBind is the recommended explicit bind for local-only access.
const defaultLoopbackBind = "127.0.0.1"

// normalizeBind preserves the legacy wildcard when bind is unset. Bracketed
// IPv6 literals, including scoped addresses, are unwrapped for JoinHostPort.
func normalizeBind(bind string) string {
	bind = strings.TrimSpace(bind)
	if bind == "" {
		return ""
	}
	if len(bind) > 1 && bind[0] == '[' && bind[len(bind)-1] == ']' {
		inner := bind[1 : len(bind)-1]
		if ip, err := netip.ParseAddr(inner); err == nil && ip.Is6() {
			return inner
		}
	}
	return bind
}

// isLoopbackBind reports whether bind restricts the listener to this host.
// Everything else — 0.0.0.0, ::, a concrete NIC address, or a hostname that
// may resolve beyond loopback — exposes the listener to the network.
func isLoopbackBind(bind string) bool {
	host := normalizeBind(bind)
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Unmap().IsLoopback()
	}
	return strings.EqualFold(host, "localhost")
}

// bindScope describes a bind address for logs and errors.
func bindScope(bind string) string {
	if isLoopbackBind(bind) {
		return "loopback"
	}
	return "non-loopback"
}

// listenAddress renders the host:port string for a configured bind address.
func listenAddress(bind string, port int) string {
	return net.JoinHostPort(normalizeBind(bind), strconv.Itoa(port))
}

// listenTCP binds the socket synchronously so that an invalid bind address, an
// out-of-range port, or a port already in use is reported to the caller before
// the server announces readiness. The caller serves on the returned listener.
func listenTCP(bind string, port int) (net.Listener, string, error) {
	addr := listenAddress(bind, port)
	if err := validateBind(bind); err != nil {
		return nil, addr, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, addr, fmt.Errorf("listen on %s: %w", addr, err)
	}
	return ln, addr, nil
}

// validateBind rejects host:port values before they become misleading listen
// errors. The port belongs in the separate port setting.
func validateBind(bind string) error {
	host := normalizeBind(bind)
	if host == "" {
		return nil
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return nil
	}
	if strings.ContainsAny(host, ":[]/ \t\r\n") {
		return fmt.Errorf("invalid bind %q: use a bare hostname or IP address, not host:port; configure the port separately (IPv6 may be bracketed)", bind)
	}
	return nil
}

// checkRemoteBindAuth enforces authentication only for explicit binds.
// Unset bind preserves legacy management and insecure bridge behavior.
func checkRemoteBindAuth(server, bind, token string) error {
	if err := validateBind(bind); err != nil {
		return fmt.Errorf("%s: %w", server, err)
	}
	if normalizeBind(bind) == "" || strings.TrimSpace(token) != "" || isLoopbackBind(bind) {
		return nil
	}
	return fmt.Errorf(
		"%s: refusing to listen on non-loopback address %q without a token: "+
			"set %s.token, or bind to %s",
		server, normalizeBind(bind), server, defaultLoopbackBind)
}
