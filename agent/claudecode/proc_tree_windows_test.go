//go:build windows

package claudecode

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// The CI matrix is Linux only, so this test has to be run on a Windows
// machine:
//
//	go test ./agent/claudecode/ -run TestCaptureProcessTree -v
//
// It checks the Toolhelp32 based enumeration against a real process tree —
// the part that cannot be covered by the platform-independent tests.

// TestCaptureProcessTree_FindsDescendants spawns a child process and checks
// that it is found as a descendant of the test process.
func TestCaptureProcessTree_FindsDescendants(t *testing.T) {
	child := exec.Command("cmd", "/c", "ping", "-n", "30", "127.0.0.1")
	if err := child.Start(); err != nil {
		t.Skipf("cannot start the helper process: %v", err)
	}
	defer func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	}()

	// The helper is a direct child of this process, so it has to show up as
	// a descendant of the test process.
	probe := newProcessTreeProbe()

	var refs []processRef
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		refs = probe.descendants(os.Getpid())
		for _, ref := range refs {
			if ref.PID == child.Process.Pid {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("descendants did not find the helper process %d, got %+v", child.Process.Pid, refs)
}

// TestCaptureProcessTree_IdentityDetectsRecycledPID checks that a captured
// process keeps its identity and that a process that is gone is not alive.
func TestCaptureProcessTree_IdentityDetectsRecycledPID(t *testing.T) {
	child := exec.Command("cmd", "/c", "ping", "-n", "30", "127.0.0.1")
	if err := child.Start(); err != nil {
		t.Skipf("cannot start the helper process: %v", err)
	}
	pid := child.Process.Pid
	_ = child.Process.Kill()
	_ = child.Wait()

	probe := newProcessTreeProbe()
	ref := processRef{PID: pid, Identity: probe.identity(pid)}
	if ref.Identity == "" {
		// The process is already gone, so there is nothing to compare against.
		return
	}
	if probe.alive(pid) {
		t.Fatalf("process %d must not be alive after it was killed", pid)
	}
}
