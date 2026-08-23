//go:build windows

package workspacechat

import (
	"os"

	"golang.org/x/sys/windows"
)

func lockInitializationFile(file *os.File) error {
	overlapped := &windows.Overlapped{}
	return windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK,
		0,
		1,
		0,
		overlapped,
	)
}

func unlockInitializationFile(file *os.File) error {
	overlapped := &windows.Overlapped{}
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlapped)
}
