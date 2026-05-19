//go:build !linux

package daemon

import (
	"os"
)

// CheckLinger checks if systemd linger is enabled for the current user.
// On non-Linux platforms, this is not applicable, so we return true (enabled)
// and the current user.
func CheckLinger() (enabled bool, user string) {
	user = os.Getenv("USER")
	if user == "" {
		user = "unknown"
	}
	// Linger is a systemd concept, not applicable on macOS/Windows
	return true, user
}
