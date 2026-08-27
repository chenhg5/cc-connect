//go:build !linux && !windows

package daemon

// CheckLinger is a stub for non-Linux platforms where systemd linger does not
// apply. Returns (true, "") so callers skip the linger warning.
func CheckLinger() (enabled bool, user string) {
	return true, ""
}
