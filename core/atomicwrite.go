package core

import (
	"os"
	"path/filepath"
)

// AtomicWriteFile writes data to a file atomically when possible by first
// writing to a temporary file in the system temp directory, syncing, then
// renaming over the target.  On the same filesystem the rename is fully
// atomic.
//
// When os.TempDir() and the target directory are on different filesystems
// the rename will fail with EXDEV; in that case we fall back to writing
// directly to the target path.  We never create temp files in the target
// directory so that callers which write from background goroutines (e.g.
// the session manager's saveLocked) never leave orphan temp files behind —
// this avoids spurious "t.TempDir() cleanup: directory not empty" failures
// during parallel tests.
func AtomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dstDir := filepath.Dir(path)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}

	// Write to system temp dir for atomic rename.
	tmp, err := os.CreateTemp("", "cc-atomic-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		os.Remove(tmpPath)
		return err
	}

	// Try atomic rename (works when temp and target are on the same fs).
	if err := os.Rename(tmpPath, path); err == nil {
		return nil
	}

	// Rename failed (likely EXDEV on different mount points).
	// Fall back to direct write — safe, no orphan temp files.
	_ = os.Remove(tmpPath)
	return os.WriteFile(path, data, perm)
}
