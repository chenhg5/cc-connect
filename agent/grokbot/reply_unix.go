//go:build unix

package grokbot

import (
	"errors"
	"os"
	"syscall"
)

// openReplyFile opens path without following a symlink and without blocking
// on a FIFO. The caller fstats the returned file before reading it.
func openReplyFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, errReplySymlink
		}
		return nil, err
	}
	return f, nil
}
