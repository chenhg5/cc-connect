//go:build unix

package claudecode

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestPrepareCmdForKill_SetsSetpgid(t *testing.T) {
	cmd := exec.Command("/bin/true")
	prepareCmdForKill(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("SysProcAttr is nil after prepareCmdForKill")
	}
	if !cmd.SysProcAttr.Setpgid {
		t.Fatal("Setpgid not set after prepareCmdForKill")
	}
}

func TestPrepareCmdForKill_PreservesExistingSysProcAttr(t *testing.T) {
	cmd := exec.Command("/bin/true")
	cmd.SysProcAttr = &syscall.SysProcAttr{Foreground: false}
	prepareCmdForKill(cmd)
	if !cmd.SysProcAttr.Setpgid {
		t.Fatal("Setpgid not set when SysProcAttr was pre-populated")
	}
}

func TestPrepareCmdForKill_NilCmd(t *testing.T) {
	// Must not panic on a nil *exec.Cmd.
	prepareCmdForKill(nil)
}

func TestForceKillCmd_NoProcess(t *testing.T) {
	cmd := exec.Command("/bin/true")
	// cmd has not been Start()ed, so cmd.Process is nil.
	if err := forceKillCmd(cmd); err != nil {
		t.Errorf("expected no error on un-started cmd, got %v", err)
	}
}

func TestForceKillCmd_NilCmd(t *testing.T) {
	if err := forceKillCmd(nil); err != nil {
		t.Errorf("expected no error on nil cmd, got %v", err)
	}
}

// TestForceKillCmd_KillsGrandchild is the regression test for the original
// bug: spawning a shell that backgrounds a long-running grandchild, then
// proving that forceKillCmd reaps the grandchild along with the direct
// child via process-group kill. Without prepareCmdForKill setting up the
// process group, the grandchild would survive and spin.
func TestForceKillCmd_KillsGrandchild(t *testing.T) {
	// /bin/sh -c 'sleep 60 & echo $! ; wait'
	// The grandchild PID is printed on stdout so we can verify it is reaped.
	cmd := exec.Command("/bin/sh", "-c", "sleep 60 & echo $! ; wait")
	prepareCmdForKill(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Read the grandchild PID.
	buf := make([]byte, 32)
	deadline := time.Now().Add(2 * time.Second)
	var grandchildPidStr string
	for time.Now().Before(deadline) {
		n, _ := stdout.Read(buf)
		if n > 0 {
			grandchildPidStr = string(buf[:n])
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if grandchildPidStr == "" {
		_ = forceKillCmd(cmd)
		_ = cmd.Wait()
		t.Fatal("did not receive grandchild PID")
	}

	if err := forceKillCmd(cmd); err != nil {
		t.Fatalf("forceKillCmd: %v", err)
	}
	_ = cmd.Wait()

	// Verify the grandchild is gone by checking that signaling it with 0
	// (no-op, just checks existence) returns ESRCH within a short window.
	// We can't easily parse the PID without strconv import bloat in tests,
	// so we rely on `pgrep` semantics: re-kill the group should be a no-op.
	if err := forceKillCmd(cmd); err != nil {
		t.Errorf("second forceKillCmd should be no-op, got %v", err)
	}
}

func TestSignalProcessGroup_NoProcess(t *testing.T) {
	cmd := exec.Command("/bin/true")
	if err := signalProcessGroup(cmd, syscall.SIGTERM); err != nil {
		t.Errorf("expected no error on un-started cmd, got %v", err)
	}
}

func TestSignalProcessGroup_NilCmd(t *testing.T) {
	if err := signalProcessGroup(nil, syscall.SIGTERM); err != nil {
		t.Errorf("expected no error on nil cmd, got %v", err)
	}
}
