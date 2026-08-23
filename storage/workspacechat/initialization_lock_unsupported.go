//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package workspacechat

import (
	"fmt"
	"os"
	"runtime"
)

func lockInitializationFile(_ *os.File) error {
	return fmt.Errorf("workspace chat initialization locking is not supported on %s", runtime.GOOS)
}

func unlockInitializationFile(_ *os.File) error {
	return nil
}
