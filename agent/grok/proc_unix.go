//go:build linux || darwin

package grok

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// Grok can launch shell commands, MCP servers, and subagents. Put each
// headless turn in its own process group so /stop and shutdown cannot orphan
// those descendants.
func prepareCmdForKill(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

func forceKillCmd(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		if errors.Is(err, syscall.ESRCH) {
			err = cmd.Process.Kill()
			if errors.Is(err, os.ErrProcessDone) {
				return nil
			}
		}
		return err
	}
	return nil
}
