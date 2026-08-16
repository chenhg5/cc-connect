//go:build windows

package main

import (
	"fmt"
	"os"
)

// runDoctorUserIsolation reports that run_as_user isolation does not exist on
// Windows. The rest of `cc-connect-next doctor` runs normally there.
func runDoctorUserIsolation(_ []string) {
	fmt.Fprintln(os.Stderr, "doctor user-isolation: run_as_user is not supported on Windows")
	os.Exit(1)
}
