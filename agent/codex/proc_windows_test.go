//go:build windows

package codex

import (
	"os"
	"os/exec"
	"syscall"
	"testing"

	"github.com/chenhg5/cc-connect/core"
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

// Assert the flags on the child actually started by connect, not just on a
// detached exec.Cmd, and check that stdout still carries the JSON-RPC event.
func TestAppServerSession_DoesNotCreateConsole(t *testing.T) {
	s := newTransportTestSession(t)
	s.cliBin = os.Args[0]
	s.cliExtraArgs = []string{"-test.run=^TestAppServerTransportProcessHelper$", "--"}
	s.workDir = t.TempDir()
	s.url = "stdio://"
	s.extraEnv = []string{"CC_CONNECT_TRANSPORT_HELPER=completed", "GORACE=atexit_sleep_ms=0"}
	if err := s.connect(); err != nil {
		t.Fatal(err)
	}
	attr := s.cmd.SysProcAttr
	if attr == nil || !attr.HideWindow || attr.CreationFlags&0x08000000 == 0 || attr.CreationFlags&syscall.CREATE_NEW_PROCESS_GROUP != 0 {
		t.Fatalf("app-server console flags = %+v", attr)
	}
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	awaitTransportTest(t, done)
	select {
	case event := <-s.events:
		if event.Type != core.EventResult {
			t.Fatalf("app-server stdout terminal event = %+v", event)
		}
	default:
		t.Fatal("app-server stdout did not deliver a terminal event")
	}
}
