//go:build !linux

package daemon

// CheckLinger is only meaningful for user-level systemd services on Linux.
func CheckLinger() (enabled bool, user string) {
	return true, ""
}
