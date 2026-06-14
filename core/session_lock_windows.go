//go:build windows

package core

import (
	"log/slog"
	"os"
)

// acquireStoreLock is a best-effort no-op on Windows. flock(2) is not
// portable, and Windows file locking (LockFileEx) has different
// semantics: it only blocks other LockFileEx callers, not all writers,
// so it does not protect against a torn JSON write from a non-cooperating
// process. The in-process sm.mu remains the primary defense against
// concurrent access within the cc-connect process. Callers may still
// observe cross-process races on Windows; that is a known limitation
// documented in issue #324. We warn once at first use so operators can
// see why cross-process concurrency is not guarded here.
var sessionLockWarned bool

func acquireStoreLock(storePath string) func() {
	if storePath == "" {
		return func() {}
	}
	if !sessionLockWarned {
		slog.Warn("session: cross-process flock is not supported on Windows; using in-process mutex only",
			"path", storePath, "issue", "#324")
		sessionLockWarned = true
	}
	// Best-effort: still create the lock file so saveLocked/load have
	// a stable sidecar path; ignore any failure since we cannot lock it.
	if _, err := os.OpenFile(storePath+".lock", os.O_CREATE|os.O_RDWR, 0o644); err != nil {
		slog.Warn("session: cannot create lock file on Windows", "path", storePath+".lock", "error", err)
	}
	return func() {}
}
