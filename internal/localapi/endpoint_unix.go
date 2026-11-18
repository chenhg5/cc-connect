//go:build !windows

package localapi

import (
	"context"
	"net"
	"path/filepath"
)

// Path names the endpoint a local API client should open.
func Path(dataDir string) string {
	return filepath.Join(dataDir, "run", "api.sock")
}

// DialContext connects only to the local Unix socket at endpointPath.
func DialContext(ctx context.Context, endpointPath string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "unix", endpointPath)
}
