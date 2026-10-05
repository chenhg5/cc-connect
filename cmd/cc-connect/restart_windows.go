//go:build windows

package main

import (
	"os"
	"os/exec"
)

func restartProcess(execPath string) error {
	if os.Getenv("CC_DAEMON_WINDOWS_SUPERVISED") == "1" {
		// The launcher must own the replacement process and its PID, including /restart.
		os.Exit(75)
	}
	cmd := exec.Command(execPath, os.Args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = os.Environ()
	return cmd.Start()
}
