//go:build unix

package opencode

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// prepareCmdForKill puts the spawned child into its own process group so the
// whole descendant tree can be signalled with one signal aimed at the negative
// PID. `opencode serve` spawns children of its own (LSP servers, tool
// subprocesses); signalling only the direct child would leave them as orphans
// after the server is reaped.
//
// Mirrors the pattern used by agent/claudecode/proc_unix.go and
// agent/codex/proc_unix.go.
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
	if err := syscall.Kill(-cmd.Process.Pid, sig); err != nil &&
		!errors.Is(err, os.ErrProcessDone) &&
		!errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

// terminateCmd asks the whole process group to shut down. SIGTERM (rather than
// SIGINT) is deliberate: a background child of a non-interactive shell — the shape
// of many server grandchildren — ignores SIGINT by default, so only SIGTERM
// actually reaches the whole tree.
func terminateCmd(cmd *exec.Cmd) error {
	return signalProcessGroup(cmd, syscall.SIGTERM)
}

// forceKillCmd SIGKILLs the entire process group rooted at cmd.
func forceKillCmd(cmd *exec.Cmd) error {
	return signalProcessGroup(cmd, syscall.SIGKILL)
}
