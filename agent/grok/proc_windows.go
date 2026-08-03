//go:build windows

package grok

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

const windowsTaskkillTimeout = 3 * time.Second

func prepareCmdForKill(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}

func forceKillCmd(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return killWindowsProcessTree(
		cmd.Process.Pid,
		func(name string, args ...string) error {
			killCtx, cancel := context.WithTimeout(context.Background(), windowsTaskkillTimeout)
			defer cancel()
			output, err := exec.CommandContext(killCtx, name, args...).CombinedOutput()
			if err == nil {
				return nil
			}
			if killCtx.Err() != nil {
				return fmt.Errorf("%s timed out: %w", name, killCtx.Err())
			}
			detail := strings.TrimSpace(string(output))
			if detail == "" {
				return err
			}
			return fmt.Errorf("%w: %s", err, detail)
		},
		func() error {
			err := cmd.Process.Kill()
			if errors.Is(err, os.ErrProcessDone) {
				return nil
			}
			return err
		},
	)
}
