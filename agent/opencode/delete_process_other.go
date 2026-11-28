//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly

package opencode

import "os/exec"

// CommandContext terminates and Wait reaps the direct process. The CLI must
// not delegate deletion to detached workers on platforms without group cleanup.
func configureDeleteProcess(cmd *exec.Cmd) func() error { return func() error { return nil } }
