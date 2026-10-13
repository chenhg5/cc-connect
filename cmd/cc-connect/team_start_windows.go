//go:build windows

package main

import (
	"os"
	"os/exec"
)

func handoffTeamService(executable string, args []string) error {
	cmd := exec.Command(executable, args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = os.Environ()
	// Wait in the foreground; never leave an untracked background service.
	return cmd.Run()
}
