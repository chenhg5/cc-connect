//go:build !windows

package main

import (
	"os"
	"syscall"
)

// Replace the initializer, retaining the foreground terminal and process ID.
func handoffTeamService(executable string, args []string) error {
	return syscall.Exec(executable, args, os.Environ())
}
