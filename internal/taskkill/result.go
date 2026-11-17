// Package taskkill classifies taskkill command results without relying on
// localized diagnostic text.
package taskkill

import (
	"errors"
	"os/exec"
)

// IsNotRunning reports whether taskkill found no running target process.
// Only exit status 128 is accepted; launch errors and other nonzero statuses
// must retain the caller's fallback and error-reporting behavior.
func IsNotRunning(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == 128
}
