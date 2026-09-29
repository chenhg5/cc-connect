//go:build unix

package core

import (
	"errors"
	"os/exec"
	"syscall"
)

// prepareShellCmdForKill puts the shell spawned for a command into its own
// process group so that the entire descendant tree can be terminated with a
// single signal aimed at the negative PID. Without this, a timeout can only
// signal the direct child, leaving grandchildren (for example the `sleep` in
// `sh -c "sleep 5"` on Linux, where /bin/sh forks instead of exec'ing) alive
// and holding the inherited stdout/stderr pipes.
//
// Mirrors the pattern used by agent/codex/proc_unix.go and
// agent/claudecode/proc_unix.go.
func prepareShellCmdForKill(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killShellProcessGroup SIGKILLs the entire process group rooted at cmd,
// falling back to killing the direct child when the group is already gone or
// could not be signalled.
func killShellProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	// syscall.Kill returns ESRCH when the group no longer exists (the process
	// has already been reaped), in which case there is nothing left to kill.
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if err == nil || errors.Is(err, syscall.ESRCH) {
		return
	}
	_ = cmd.Process.Kill()
}
