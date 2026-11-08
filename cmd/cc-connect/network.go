package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"
)

// configureProcessDNS provides an opt-in resolver for this process without
// changing the host's DNS, proxy, or routing configuration.
func configureProcessDNS(server string) error {
	server = strings.TrimSpace(server)
	if server == "" {
		return nil
	}
	host, port, err := net.SplitHostPort(server)
	if err != nil {
		return fmt.Errorf("CC_CONNECT_DNS_SERVER must be a numeric IP address and port: %w", err)
	}
	if net.ParseIP(host) == nil {
		return fmt.Errorf("CC_CONNECT_DNS_SERVER host %q is not a numeric IP address", host)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil {
		return fmt.Errorf("CC_CONNECT_DNS_SERVER port %q is not numeric: %w", port, err)
	}
	if portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("CC_CONNECT_DNS_SERVER port %d is outside 1-65535", portNumber)
	}
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, server)
		},
	}
	slog.Info("using process-specific DNS resolver", "server", server)
	return nil
}
