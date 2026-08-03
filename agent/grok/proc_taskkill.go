package grok

import (
	"fmt"
	"strconv"
)

func windowsTaskkillCommand(pid int) (string, []string) {
	return "taskkill", []string{"/T", "/F", "/PID", strconv.Itoa(pid)}
}

func killWindowsProcessTree(pid int, run func(string, ...string) error, fallback func() error) error {
	name, args := windowsTaskkillCommand(pid)
	if err := run(name, args...); err == nil {
		return nil
	} else if fallbackErr := fallback(); fallbackErr != nil {
		return fmt.Errorf("taskkill failed: %w; process kill fallback failed: %w", err, fallbackErr)
	} else {
		// Killing only the direct process cannot guarantee that children are
		// gone. Preserve the taskkill failure so callers never report a partial
		// cleanup as successful.
		return fmt.Errorf("taskkill process-tree cleanup failed; direct process was killed: %w", err)
	}
}
