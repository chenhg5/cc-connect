package core

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// AtomicWriteFile writes data to a file atomically by first writing to a
// temporary file in the same directory, syncing, then renaming over the target.
// This prevents data loss / corruption on crash.
func AtomicWriteFile(path string, data []byte, perm os.FileMode) error {
	return atomicWriteFile(path, data, perm, nil)
}

// beforeRenameHook is invoked after the temporary file has been written,
// synced and chmodded, but before it is renamed to the final path. It is
// only used by tests to inspect the temp file while it still exists.
type beforeRenameHook func(tmpPath string) error

// atomicWriteFile is the testable implementation of AtomicWriteFile. The
// optional hook is invoked after the temp file permissions are set but before
// the rename.
func atomicWriteFile(path string, data []byte, perm os.FileMode, hook beforeRenameHook) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	tmpPattern := fmt.Sprintf(".%s.*.tmp.%d.%d", base, os.Getpid(), time.Now().UnixNano())
	tmp, err := os.CreateTemp(dir, tmpPattern)
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
	if hook != nil {
		if err := hook(tmpPath); err != nil {
			os.Remove(tmpPath)
			return err
		}
	}
	return os.Rename(tmpPath, path)
}
