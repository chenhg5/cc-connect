//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestAcquireInstanceLock_Success(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")

	lock, err := AcquireInstanceLock(cfg)
	if err != nil {
		t.Fatalf("AcquireInstanceLock: %v", err)
	}
	if lock == nil || !lock.acquired {
		t.Fatal("expected acquired lock")
	}
	defer lock.Release()

	if lock.Path() == "" {
		t.Fatal("expected non-empty lock path")
	}
}

func TestAcquireInstanceLock_AlreadyLocked(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")

	first, err := AcquireInstanceLock(cfg)
	if err != nil {
		t.Fatalf("first AcquireInstanceLock: %v", err)
	}
	defer first.Release()

	_, err = AcquireInstanceLock(cfg)
	if err == nil {
		t.Fatal("second AcquireInstanceLock should fail while lock held")
	}
}

// TestMain doubles as the helper-process entry point for
// TestKillExistingInstance_WaitsForExit. When INSTANCE_LOCK_HELPER
// is set, the binary acquires the lock at the given path and blocks
// until SIGKILL; otherwise it runs the normal test suite.
func TestMain(m *testing.M) {
	if cfg := os.Getenv("INSTANCE_LOCK_HELPER"); cfg != "" {
		lock, err := AcquireInstanceLock(cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "helper: AcquireInstanceLock: %v\n", err)
			os.Exit(2)
		}
		// Signal readiness to the parent by writing the PID file
		// (already done inside AcquireInstanceLock) and parking until
		// the parent SIGKILLs us. select{} blocks forever; SIGKILL
		// terminates the process immediately and releases the flock.
		_ = lock
		select {}
	}
	os.Exit(m.Run())
}

// TestKillExistingInstance_WaitsForExit pins the bug where
// KillExistingInstance returned immediately after proc.Kill() — only
// queuing SIGKILL, not waiting for the kernel to reap the child and
// release the file lock. With the fix the function polls Signal(0)
// until the process is gone, so an immediate-following
// AcquireInstanceLock can succeed against the freed flock (which is
// exactly the `cc-connect --force` user flow).
func TestKillExistingInstance_WaitsForExit(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	// Spawn the helper, which acquires the lock and blocks.
	helper := exec.Command(exe, "-test.run=TestMain$")
	helper.Env = append(os.Environ(), "INSTANCE_LOCK_HELPER="+cfg)
	if err := helper.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	// Best-effort cleanup if KillExistingInstance returns false.
	t.Cleanup(func() {
		if helper.Process != nil {
			_ = helper.Process.Kill()
			_, _ = helper.Process.Wait()
		}
	})

	// Wait for the helper to acquire the lock. Poll the lock file
	// until it contains a non-zero PID. Bound to avoid hanging if the
	// helper crashes.
	lockPath := filepath.Join(dir, ".config.toml.lock")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if pid := readPIDFromLockFile(lockPath); pid > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if pid := readPIDFromLockFile(lockPath); pid <= 0 {
		t.Fatal("helper did not acquire lock in time")
	}

	if !KillExistingInstance(cfg) {
		t.Fatal("KillExistingInstance returned false")
	}

	// Immediately after KillExistingInstance returns, the lock MUST
	// be acquirable. Without the wait-for-exit fix, this would race
	// the kernel and frequently fail because the SIGKILLed helper's
	// flock is still held.
	lock, err := AcquireInstanceLock(cfg)
	if err != nil {
		t.Fatalf("AcquireInstanceLock immediately after KillExistingInstance: %v", err)
	}
	lock.Release()
}
