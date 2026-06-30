//go:build !linux

package daemon

import "os"

// CheckLinger is a no-op on non-Linux platforms. Linger is a systemd concept
// that only applies to Linux user services. Returns true so callers treat the
// state as "no action needed".
func CheckLinger() (enabled bool, user string) {
	user = os.Getenv("USER")
	if user == "" {
		user = "unknown"
	}
	return true, user
}
