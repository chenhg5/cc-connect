//go:build windows

package codex

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

const createNoWindow uint32 = 0x08000000

func prepareCmdForKill(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP | createNoWindow
}

func forceKillCmd(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	killCmd := taskkillCommand(cmd.Process.Pid)
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

func taskkillCommand(pid int) *exec.Cmd {
	command := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid))
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	return command
}

func processKillOutput(output []byte) string {
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return "(empty output)"
	}
	return trimmed
}
