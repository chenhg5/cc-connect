//go:build !windows

package main

import (
	"os"
	"syscall"
)

func restartProcess(execPath string) error {
	return syscall.Exec(execPath, os.Args, os.Environ())
}
