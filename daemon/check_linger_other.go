//go:build !linux && !darwin

package daemon

// CheckLinger is a stub for platforms where systemd linger does not apply and
// there is no dedicated platform file (darwin provides launchd.go). Returns
// (true, "") so callers skip the linger warning.
func CheckLinger() (enabled bool, user string) {
	return true, ""
}
