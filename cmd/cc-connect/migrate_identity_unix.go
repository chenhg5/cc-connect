//go:build linux || darwin

package main

import (
	"io/fs"
	"syscall"
)

func migrationOwnership(info fs.FileInfo) (int, int, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(stat.Uid), int(stat.Gid), true
}
