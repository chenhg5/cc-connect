package core

import (
	"errors"
	"os"
	"syscall"
)

func apiSocketConnectionRefused(err error) bool {
	// Winsock returns WSAECONNREFUSED, not Go's synthetic ECONNREFUSED.
	const wsaeconnrefused syscall.Errno = 10061
	return errors.Is(err, wsaeconnrefused)
}

func lockAPISocket(path string) (*os.File, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ, nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}
