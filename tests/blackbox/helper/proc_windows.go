//go:build windows

package helper

import "os/exec"

// Windows has no POSIX process groups; stop the cc-connect process itself.
func setProcessGroup(cmd *exec.Cmd) {}

func terminateProcessGroup(cmd *exec.Cmd) { _ = cmd.Process.Kill() }

func killProcessGroup(cmd *exec.Cmd) { _ = cmd.Process.Kill() }
