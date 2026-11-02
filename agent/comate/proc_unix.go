//go:build unix

package comate

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// prepareCmdForKill puts the comatecli process into its own process group
// so a later force-kill can reap the CLI together with any grandchildren
// (comatecli spawns tool subprocesses that would otherwise survive).
func prepareCmdForKill(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// forceKillCmd SIGKILLs the whole process group (-pid). A negative pid
// targets the group leader and all members; ESRCH/ErrProcessDone mean the
// group already exited and are treated as success.
func forceKillCmd(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
