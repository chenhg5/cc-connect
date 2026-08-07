//go:build unix

package grok

import (
	"errors"
	"fmt"
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
	groupErr := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if groupErr == nil || errors.Is(groupErr, os.ErrProcessDone) {
		return nil
	}
	// If process-group signaling is unavailable or races with process exit,
	// still guarantee that the direct Grok process is terminated.
	directErr := cmd.Process.Kill()
	if errors.Is(groupErr, syscall.ESRCH) && errors.Is(directErr, os.ErrProcessDone) {
		return nil
	}
	if directErr != nil && !errors.Is(directErr, os.ErrProcessDone) {
		return fmt.Errorf("process-group kill failed: %w; direct process kill failed: %w", groupErr, directErr)
	}
	return fmt.Errorf("process-group cleanup failed; direct process was killed: %w", groupErr)
}
