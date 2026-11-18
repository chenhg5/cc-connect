//go:build !windows

package core

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

func listenLocalAPI(dataDir string) (apiEndpoint, error) {
	sockDir := filepath.Join(dataDir, "run")
	if err := os.MkdirAll(sockDir, 0o755); err != nil {
		return apiEndpoint{}, fmt.Errorf("create run dir: %w", err)
	}
	sockPath := filepath.Join(sockDir, "api.sock")
	// Keep the lock for the listener lifetime, including stale-socket recovery.
	lock, err := lockAPISocket(sockPath + ".lock")
	if err != nil {
		return apiEndpoint{}, fmt.Errorf("lock API socket: %w", err)
	}
	owned := false
	defer func() {
		if !owned {
			_ = lock.Close()
		}
	}()
	if info, err := os.Lstat(sockPath); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return apiEndpoint{}, fmt.Errorf("API socket path is not a socket: %s", sockPath)
		}
		conn, dialErr := net.DialTimeout("unix", sockPath, time.Second)
		if dialErr == nil {
			_ = conn.Close()
			return apiEndpoint{}, fmt.Errorf("API socket is already listening: %s", sockPath)
		}
		if !apiSocketConnectionRefused(dialErr) && !errors.Is(dialErr, os.ErrNotExist) {
			return apiEndpoint{}, fmt.Errorf("check existing API socket: %w", dialErr)
		}
		if err := removeAPISocket(sockPath, info); err != nil {
			return apiEndpoint{}, fmt.Errorf("remove stale API socket: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return apiEndpoint{}, fmt.Errorf("stat API socket: %w", err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: sockPath, Net: "unix"})
	if err != nil {
		return apiEndpoint{}, fmt.Errorf("listen unix socket: %w", err)
	}
	listener.SetUnlinkOnClose(false)
	info, err := os.Lstat(sockPath)
	if err != nil {
		_ = listener.Close()
		return apiEndpoint{}, fmt.Errorf("stat new API socket: %w", err)
	}
	if err := os.Chmod(sockPath, 0o600); err != nil {
		_ = listener.Close()
		_ = removeAPISocket(sockPath, info)
		return apiEndpoint{}, fmt.Errorf("chmod socket: %w", err)
	}
	owned = true
	return apiEndpoint{
		path: sockPath, listener: listener,
		cleanup: func() error {
			return errors.Join(removeAPISocket(sockPath, info), lock.Close())
		},
	}, nil
}

func removeAPISocket(path string, owned os.FileInfo) error {
	if owned == nil {
		return nil
	}
	current, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !os.SameFile(owned, current) {
		return nil
	}
	return os.Remove(path)
}
