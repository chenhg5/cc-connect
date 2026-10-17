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
	portNumber, portErr := strconv.Atoi(port)
	if err != nil || net.ParseIP(host) == nil || portErr != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("CC_CONNECT_DNS_SERVER must be an IP address and port")
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
