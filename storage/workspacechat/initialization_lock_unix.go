//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package workspacechat

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func lockInitializationFile(file *os.File) error {
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

func unlockInitializationFile(file *os.File) error {
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_UN)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}
