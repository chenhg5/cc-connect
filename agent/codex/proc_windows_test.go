//go:build windows

package codex

import (
	"os/exec"
	"syscall"
	"testing"
)

func TestPrepareCmdForKillDoesNotCreateConsole(t *testing.T) {
	prepareCmdForKill(nil)
	command := exec.Command("synthetic-codex.exe")
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_UNICODE_ENVIRONMENT}
	prepareCmdForKill(command)
	required := uint32(syscall.CREATE_NEW_PROCESS_GROUP | 0x08000000 | syscall.CREATE_UNICODE_ENVIRONMENT)
	if !command.SysProcAttr.HideWindow || command.SysProcAttr.CreationFlags&required != required {
		t.Fatalf("console flags not preserved: %+v", command.SysProcAttr)
	}
}

func TestTaskkillCommandDoesNotCreateConsole(t *testing.T) {
	command := taskkillCommand(123)
	if command.SysProcAttr == nil || !command.SysProcAttr.HideWindow || command.SysProcAttr.CreationFlags&0x08000000 == 0 {
		t.Fatal("taskkill must run without a console")
	}
	want := []string{"taskkill", "/T", "/F", "/PID", "123"}
	if len(command.Args) != len(want) {
		t.Fatalf("unexpected argv: %v", command.Args)
	}
	for i := range want {
		if command.Args[i] != want[i] {
			t.Fatalf("unexpected argv: %v", command.Args)
		}
	}
}
