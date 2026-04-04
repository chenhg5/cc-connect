//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// InstanceLock is a no-op on Windows for now.
// TODO: implement proper Windows locking using CreateFile with exclusive mode.
type InstanceLock struct {
	path string
}

// AcquireInstanceLock attempts to acquire an exclusive lock for the given config path.
// On Windows, this currently always succeeds (no-op).
func AcquireInstanceLock(configPath string) (*InstanceLock, error) {
	configDir := filepath.Dir(configPath)
	configBase := filepath.Base(configPath)
	lockName := fmt.Sprintf(".%s.lock", configBase)
	lockPath := filepath.Join(configDir, lockName)

	// Write our PID to the lock file for diagnostics
	pid := os.Getpid()
	if err := os.WriteFile(lockPath, []byte(fmt.Sprintf("%d\n", pid)), 0644); err != nil {
		// Non-fatal on Windows
	}

	return &InstanceLock{path: lockPath}, nil
}

// Release releases the instance lock.
func (l *InstanceLock) Release() {
	if l == nil {
		return
	}
	// Remove lock file
	if l.path != "" {
		os.Remove(l.path)
	}
}

// Path returns the path to the lock file.
func (l *InstanceLock) Path() string {
	return l.path
}

// KillExistingInstance is not implemented on Windows.
func KillExistingInstance(configPath string) bool {
	return false
}