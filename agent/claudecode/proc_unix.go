//go:build unix

package claudecode

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// prepareCmdForKill puts the spawned child into its own process group so that
// the entire descendant tree can be terminated with a single signal aimed at
// the negative PID. Without this, cc-connect can only signal the direct
// child (e.g. the `claude` CLI), leaving any grandchildren (MCP server
// processes such as the Telegram bridge) as orphans that may spin at 100%
// CPU when their parent disappears.
//
// Mirrors the pattern used by agent/codex/proc_unix.go.
func prepareCmdForKill(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// signalProcessGroup sends sig to the entire process group rooted at cmd.
// Returns nil if the group is already gone.
func signalProcessGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, sig)
	if err == nil || errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	// macOS can return EPERM for a stale negative PGID after Wait has reaped
	// the direct child. Only classify that case as idempotent after the
	// os.Process state independently confirms the child is already done.
	if errors.Is(err, syscall.EPERM) && errors.Is(cmd.Process.Signal(syscall.Signal(0)), os.ErrProcessDone) {
		return nil
	}
	return err
}

// forceKillCmd SIGKILLs the entire process group rooted at cmd. Use this
// as the last-resort escalation when graceful shutdown has timed out.
func forceKillCmd(cmd *exec.Cmd) error {
	return signalProcessGroup(cmd, syscall.SIGKILL)
}
