//go:build windows

package core

// isMountPoint: the media library NAS check is macOS/Unix only.
func isMountPoint(string) bool { return false }
