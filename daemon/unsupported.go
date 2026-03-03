//go:build !linux && !darwin

package daemon

import (
	"fmt"
	"runtime"
)

func newPlatformManager() (Manager, error) {
	return nil, fmt.Errorf("daemon management is not supported on %s; use a process manager (e.g. nssm, pm2) instead", runtime.GOOS)
}
