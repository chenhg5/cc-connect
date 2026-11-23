//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package opencode

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// Keep ordinary descendants in a private process group, including children
// holding inherited output pipes. This cannot contain a child that deliberately
// detaches into another group/session; the CLI must not daemonize deletion.
func configureDeleteProcess(cmd *exec.Cmd) func() error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	killGroup := func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.Cancel = killGroup
	return func() error {
		if cmd.Process == nil {
			return nil
		}
		err := killGroup()
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		if err != nil {
			return err
		}
		// A group still existed after the direct process was reaped.
		// Signal delivery alone cannot establish its deletion outcome.
		return errors.New("residual deletion process group terminated; outcome uncertain")
	}
}
