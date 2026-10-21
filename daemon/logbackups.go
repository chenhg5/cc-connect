package daemon

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseLogBackups converts a string into a positive integer count of log
// backups to retain when the rotating writer rotates the active log file.
// Unlike ParseLogSize, no unit suffix is accepted — a backup count is
// already an integer in any sane unit. Whitespace is trimmed.
//
// Returns an error if the input is empty, non-integer, or not >= 1. The
// minimum is 1 (one backup, the legacy behaviour); zero would mean
// "discard the previous log on every rotation", which loses the entire
// post-mortem trail at the moment something goes wrong.
//
// A typical rotation policy with N=3 and maxSize=10MB keeps cc-connect.log
// plus cc-connect.log.1 / .2 / .3 on disk, so the maximum retained footprint
// is ≈ 4 × maxSize.
func ParseLogBackups(s string) (int, error) {
	orig := s
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("log backups: empty value")
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("log backups %q: %w", orig, err)
	}
	if n < 1 {
		return 0, fmt.Errorf("log backups %q: must be >= 1 (got %d)", orig, n)
	}
	return n, nil
}
