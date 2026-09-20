//go:build windows

package core

// grantSocketAccess is a no-op on Windows: run_as_user is rejected at config
// parse time, and Windows has no equivalent Unix ownership model for the socket.
func grantSocketAccess(sockPath string, runAsUsers []string) error {
	return nil
}
