//go:build windows

package core

import "os/exec"

// isMountPoint: the media library NAS check is macOS/Unix only.
func isMountPoint(string) bool { return false }

// killGroupOnCancel: default CommandContext kill on Windows.
func killGroupOnCancel(*exec.Cmd) {}
