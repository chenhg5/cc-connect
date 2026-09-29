package core

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// defaultLoopbackBind keeps the optional HTTP listeners (Management API, Web
// UI, Bridge) reachable from this host only. Remote deployments must opt in by
// setting bind explicitly.
const defaultLoopbackBind = "127.0.0.1"

// normalizeBind trims the configured bind address and applies the secure
// loopback default. Bracketed IPv6 literals ("[::]", "[::1]") are accepted and
// unwrapped, because net.JoinHostPort re-adds the brackets and would otherwise
// turn "[::]" into the invalid "[[::]]:port".
func normalizeBind(bind string) string {
	bind = strings.TrimSpace(bind)
	if bind == "" {
		return defaultLoopbackBind
	}
	if len(bind) > 1 && bind[0] == '[' && bind[len(bind)-1] == ']' {
		if inner := bind[1 : len(bind)-1]; net.ParseIP(inner) != nil {
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
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
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
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, addr, fmt.Errorf("listen on %s: %w", addr, err)
	}
	return ln, addr, nil
}

// checkRemoteBindAuth rejects a listener that would be exposed beyond loopback
// without authentication. An empty token is only safe on loopback, so insecure
// development mode cannot accidentally publish an unauthenticated API.
func checkRemoteBindAuth(server, bind, token string) error {
	if token != "" || isLoopbackBind(bind) {
		return nil
	}
	return fmt.Errorf(
		"%s: refusing to listen on non-loopback address %q without a token: "+
			"set %s.token, or bind to %s",
		server, normalizeBind(bind), server, defaultLoopbackBind)
}
