//go:build !windows

package core

import (
	"errors"
	"os"
	"syscall"
)

func apiSocketConnectionRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED)
}

func lockAPISocket(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, err
	}
	// Keep the lock file in place: unlinking it would let another process
	// lock a different inode while a previous owner still holds this one.
	return file, nil
}
