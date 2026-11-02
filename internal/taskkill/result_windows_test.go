//go:build windows

package taskkill

import (
	"os/exec"
	"testing"
)

func TestTaskkillNotRunning_MissingPID(t *testing.T) {
	// Exercise the real command with a deliberately high, odd PID, including
	// on localized Windows installations.
	for _, args := range [][]string{
		{"/T", "/F", "/PID", "2147483647"},
		{"/T", "/PID", "2147483647"},
	} {
		output, err := exec.Command("taskkill", args...).CombinedOutput()
		if !IsNotRunning(err) {
			t.Fatalf("taskkill %v: expected absent-process exit status, got %v: %s", args, err, output)
		}
	}
}
