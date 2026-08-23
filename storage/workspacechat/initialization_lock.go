package workspacechat

import (
	"errors"
	"fmt"
	"os"
)

type initializationLock struct {
	file *os.File
}

func acquireInitializationLock(path string) (*initializationLock, error) {
	file, err := openInitializationLockFile(path)
	if err != nil {
		return nil, err
	}
	if err := lockInitializationFile(file); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return &initializationLock{file: file}, nil
}

func openInitializationLockFile(path string) (*os.File, error) {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("lock path is not a regular file: %s", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect lock file: %w", err)
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	closeWithError := func(operationErr error) (*os.File, error) {
		return nil, errors.Join(operationErr, file.Close())
	}

	openedInfo, err := file.Stat()
	if err != nil {
		return closeWithError(fmt.Errorf("inspect opened lock file: %w", err))
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return closeWithError(fmt.Errorf("inspect installed lock file: %w", err))
	}
	if !openedInfo.Mode().IsRegular() || !pathInfo.Mode().IsRegular() || !os.SameFile(openedInfo, pathInfo) {
		return closeWithError(fmt.Errorf("lock path does not identify the opened regular file: %s", path))
	}
	return file, nil
}

func (lock *initializationLock) release() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	unlockErr := unlockInitializationFile(lock.file)
	closeErr := lock.file.Close()
	lock.file = nil
	return errors.Join(unlockErr, closeErr)
}
