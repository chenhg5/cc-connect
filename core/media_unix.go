//go:build !windows

package core

import (
	"os/exec"
	"path/filepath"
	"syscall"
)

// killGroupOnCancel starts cmd in its own process group and kills the whole group on cancel.
func killGroupOnCancel(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}

// isMountPoint reports whether path is the root of a mounted filesystem.
func isMountPoint(path string) bool {
	var st, parent syscall.Stat_t
	if syscall.Stat(path, &st) != nil || syscall.Stat(filepath.Dir(path), &parent) != nil {
		return false
	}
	return st.Dev != parent.Dev || st.Ino == parent.Ino
}
