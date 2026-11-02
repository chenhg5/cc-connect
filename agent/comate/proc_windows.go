//go:build windows

package comate

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/chenhg5/cc-connect/internal/taskkill"
)

// prepareCmdForKill puts the comatecli process into a new Windows process
// group so the tree can be force-killed later (taskkill /T).
func prepareCmdForKill(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}

// forceKillCmd kills the CLI and its whole child tree via
// `taskkill /T /F /PID`. If taskkill fails for reasons other than
// "already exited" (e.g. not on PATH), it falls back to killing just the
// top-level process handle.
func forceKillCmd(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	killCmd := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	output, err := killCmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if taskkill.IsNotRunning(err) {
		return nil
	}
	if killErr := cmd.Process.Kill(); killErr == nil || errors.Is(killErr, os.ErrProcessDone) {
		return nil
	} else {
		return fmt.Errorf("taskkill failed: %w: %s; process kill fallback failed: %w", err, processKillOutput(output), killErr)
	}
}

func processKillOutput(output []byte) string {
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return "(empty output)"
	}
	return trimmed
}
