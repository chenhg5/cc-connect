//go:build windows

package codex

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func prepareCmdForKill(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
	// The default CommandContext cancel kills only the direct child. For an
	// npm-installed codex that is the cmd.exe running codex.cmd, so node.exe
	// and codex.exe survive and keep the thread's writer lock. A later
	// taskkill /T cannot reach them either, because the tree root is gone.
	if cmd.Cancel != nil {
		cmd.Cancel = func() error { return forceKillCmd(cmd) }
	}
}

var (
	killJobOnce sync.Once
	killJob     windows.Handle
)

// attachCmdToKillJob puts a started child into a process-wide job object
// configured with KILL_ON_JOB_CLOSE, so the whole codex tree dies with
// cc-connect even when it exits without running Close (crash, restart,
// taskkill). Descendants spawned after assignment inherit the job.
func attachCmdToKillJob(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	killJobOnce.Do(func() {
		job, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			slog.Warn("codex: create kill job failed", "error", err)
			return
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
			slog.Warn("codex: configure kill job failed", "error", err)
			_ = windows.CloseHandle(job)
			return
		}
		// Intentionally never closed: the handle is released when this
		// process exits, which is what triggers the kill.
		killJob = job
	})
	if killJob == 0 {
		return
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		slog.Warn("codex: open process for kill job failed", "pid", cmd.Process.Pid, "error", err)
		return
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(killJob, h); err != nil {
		slog.Warn("codex: assign process to kill job failed", "pid", cmd.Process.Pid, "error", err)
	}
}

func forceKillCmd(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	killCmd := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	output, err := killCmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if bytes.Contains(bytes.ToLower(output), []byte("there is no running instance")) {
		return nil
	}
	if bytes.Contains(bytes.ToLower(output), []byte("not found")) {
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
