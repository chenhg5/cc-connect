//go:build !windows

package helper

import (
	"os/exec"
	"syscall"
)

// setProcessGroup starts cc-connect in its own process group so the agent
// CLIs it spawns can be stopped together with it.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateProcessGroup(cmd *exec.Cmd) { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }

func killProcessGroup(cmd *exec.Cmd) { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
