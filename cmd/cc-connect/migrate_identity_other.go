//go:build !linux && !darwin

package main

import "io/fs"

func migrationOwnership(fs.FileInfo) (int, int, bool) {
	return 0, 0, false
}
