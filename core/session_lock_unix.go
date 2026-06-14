//go:build !windows

package core

import (
	"log/slog"
	"os"

	"golang.org/x/sys/unix"
)

// acquireStoreLock takes an exclusive advisory flock(2) on a sidecar
// "<storePath>.lock" file. Returns a release function the caller must
// defer. If the lock file cannot be created or the syscall fails, the
// release function is a no-op and a warning is logged: the in-process
// sm.mu is the primary defense, this is only a best-effort
// cross-process guard (issue #324).
func acquireStoreLock(storePath string) func() {
	if storePath == "" {
		return func() {}
	}
	lockPath := storePath + ".lock"
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		slog.Warn("session: cannot create lock file, proceeding without cross-process lock", "path", lockPath, "error", err)
		return func() {}
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		slog.Warn("session: flock failed, proceeding without cross-process lock", "path", lockPath, "error", err)
		_ = f.Close()
		return func() {}
	}
	return func() {
		// Unlock is best-effort; the Close below releases the lock
		// regardless. We still call Flock(LOCK_UN) explicitly so
		// the lock is released before another process is unblocked.
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}
}
