//go:build windows

package opencode

import (
	"os/exec"
	"syscall"
)

// prepareCmdForKill starts the child in its own process group so a later kill can
// target it without touching cc-connect itself.
func prepareCmdForKill(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}

// terminateCmd stops the child. Windows has no group signal equivalent to the
// unix variant, so this degrades to killing the direct child.
func terminateCmd(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

// forceKillCmd kills the child. Windows has no process-group signal equivalent to
// the unix variant, so this degrades to killing the direct child.
func forceKillCmd(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
