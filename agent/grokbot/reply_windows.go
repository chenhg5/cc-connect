//go:build windows

package grokbot

import "os"

// openReplyFile opens a reply that consumeOutbox already Lstat'd as a regular
// file. Windows has no O_NOFOLLOW; the Lstat check is the guard, and a swap
// to a symlink between the two calls can still be followed.
func openReplyFile(path string) (*os.File, error) {
	return os.Open(path)
}
