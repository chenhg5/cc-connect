//go:build windows

package core

import (
	"os/exec"
	"strconv"
	"syscall"
)

// prepareShellCmdForKill puts the shell spawned for a command into a new
// process group on Windows so that taskkill /T can later terminate the entire
// descendant tree. The Exec.Cmd.WaitDelay backstop additionally bounds how long
// Wait blocks on inherited pipes.
//
// Mirrors the pattern used by agent/codex/proc_windows.go.
func prepareShellCmdForKill(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}

// killShellProcessGroup force-kills the descendant tree rooted at cmd via
// taskkill, falling back to killing the direct child when taskkill fails.
func killShellProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run(); err != nil {
		_ = cmd.Process.Kill()
	}
}
