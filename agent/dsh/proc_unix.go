//go:build unix

package dsh

import (
	"os/exec"
	"syscall"
)

// prepareCmdForKill puts the spawned child into its own process group so that
// the entire descendant tree can be terminated with a single signal aimed at
// the negative PID (dsh spawns worker threads / tool subprocesses).
func prepareCmdForKill(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}
