//go:build unix

package grok

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestPrepareCmdForKill_SetsProcessGroup(t *testing.T) {
	cmd := exec.Command("/bin/true")
	prepareCmdForKill(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatal("prepareCmdForKill did not create a process group")
	}
}

func TestPrepareCmdForKill_PreservesSysProcAttr(t *testing.T) {
	cmd := exec.Command("/bin/true")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	prepareCmdForKill(cmd)
	if !cmd.SysProcAttr.Setsid || !cmd.SysProcAttr.Setpgid {
		t.Fatalf("SysProcAttr = %#v, want Setsid and Setpgid", cmd.SysProcAttr)
	}
}

func TestForceKillCmd_KillsStartedProcessTree(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "sleep 60 & child=$!; printf '%s\\n' \"$child\"; wait")
	prepareCmdForKill(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	childLine, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("read child pid: %v", err)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(childLine))
	if err != nil || childPID <= 0 {
		t.Fatalf("child pid = %q: %v", childLine, err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		_ = syscall.Kill(childPID, syscall.SIGKILL)
	}()
	if err := forceKillCmd(cmd); err != nil {
		t.Fatalf("forceKillCmd: %v", err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("process exited successfully after forceKillCmd, want signal exit")
	}
	if err := forceKillCmd(cmd); err != nil {
		t.Fatalf("second forceKillCmd: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := syscall.Kill(childPID, 0)
		if errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("child process %d still exists after process-group kill: %v", childPID, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
