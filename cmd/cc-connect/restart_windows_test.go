//go:build windows

package main

import (
	"os"
	"os/exec"
	"testing"
)

func TestRestartProcess_SupervisedExit(t *testing.T) {
	if os.Getenv("CC_CONNECT_RESTART_TEST") == "1" {
		restartProcess("missing-test-binary.exe")
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRestartProcess_SupervisedExit$")
	cmd.Env = append(os.Environ(), "CC_CONNECT_RESTART_TEST=1", "CC_DAEMON_WINDOWS_SUPERVISED=1")
	err := cmd.Run()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 75 {
		t.Fatalf("supervised restart must exit with code 75 instead of spawning a detached replacement: %v", err)
	}
}
