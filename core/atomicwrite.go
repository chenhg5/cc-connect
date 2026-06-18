package core

import (
	"os"
	"path/filepath"
)

// AtomicWriteFile writes data to a file atomically by first writing to a
// temporary file in the same directory, syncing, then renaming over the target.
// This prevents data loss / corruption on crash.
func AtomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
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
	if err := os.Rename(tmpPath, path); err != nil {
		// Rename can fail when the destination is a directory, the
		// destination's filesystem differs from the temp dir's (rare given
		// CreateTemp uses the same dir, but possible with bind mounts), or
		// the destination is locked by another process on Windows. In any
		// of those cases the temp file is now an orphaned `.tmp-*` we
		// created — clean it up so repeated failures don't litter the
		// directory and confuse later directory scans (e.g. cron / session
		// stores that walk their parent dir).
		os.Remove(tmpPath)
		return err
	}
	return nil
}
