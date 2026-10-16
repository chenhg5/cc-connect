//go:build !windows

package core

import (
	"path/filepath"
	"syscall"
)

// isMountPoint reports whether path is the root of a mounted filesystem.
func isMountPoint(path string) bool {
	var st, parent syscall.Stat_t
	if syscall.Stat(path, &st) != nil || syscall.Stat(filepath.Dir(path), &parent) != nil {
		return false
	}
	return st.Dev != parent.Dev || st.Ino == parent.Ino
}
