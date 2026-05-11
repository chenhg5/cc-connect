//go:build windows

package claudecode

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// prepareCmdForKill puts the spawned child into a new process group on
// Windows so that taskkill /T can later terminate the entire descendant
// tree. Without this, cc-connect can only signal the direct child,
// leaving grandchildren (such as MCP server bridges) as orphans.
//
// Mirrors the pattern used by agent/codex/proc_windows.go.
func prepareCmdForKill(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}

// signalProcessGroup is a graceful best-effort equivalent of forceKillCmd
// on Windows: taskkill without /F asks the target to close cleanly. Falls
// back to cmd.Process.Signal if taskkill is unavailable.
func signalProcessGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	killCmd := exec.Command("taskkill", "/T", "/PID", strconv.Itoa(cmd.Process.Pid))
	output, err := killCmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if isTaskkillNotRunning(output) {
		return nil
	}
	if killErr := cmd.Process.Signal(sig); killErr == nil || errors.Is(killErr, os.ErrProcessDone) {
		return nil
	}
	return fmt.Errorf("taskkill failed: %w: %s", err, processKillOutput(output))
}

// forceKillCmd taskkill /T /F's the entire descendant tree rooted at cmd.
func forceKillCmd(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	killCmd := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	output, err := killCmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if isTaskkillNotRunning(output) {
		return nil
	}
	if killErr := cmd.Process.Kill(); killErr == nil || errors.Is(killErr, os.ErrProcessDone) {
		return nil
	} else {
		return fmt.Errorf("taskkill failed: %w: %s; process kill fallback failed: %w", err, processKillOutput(output), killErr)
	}
}

func isTaskkillNotRunning(output []byte) bool {
	lower := bytes.ToLower(output)
	return bytes.Contains(lower, []byte("there is no running instance")) ||
		bytes.Contains(lower, []byte("not found"))
}

func processKillOutput(output []byte) string {
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return "(empty output)"
	}
	return trimmed
}
