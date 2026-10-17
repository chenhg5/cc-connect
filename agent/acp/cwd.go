package acp

import (
	"path/filepath"
	"strings"
)

// canonicalCwd returns a symlink-resolved absolute path.
//
// macOS maps /tmp -> /private/tmp. Devin's session/list filter is an exact
// string match, so a cwd of "/tmp/proj" returns no sessions while
// "/private/tmp/proj" does. EvalSymlinks is applied to every cwd we send
// and to every cwd we compare. If the path does not exist, Abs+Clean is
// used so callers still have a stable absolute path.
func canonicalCwd(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil || strings.TrimSpace(resolved) == "" {
		return filepath.Clean(abs)
	}
	return filepath.Clean(resolved)
}

// sameCwd reports whether two directories refer to the same location,
// after symlink resolution, clean, and a case-insensitive comparison.
func sameCwd(a, b string) bool {
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return false
	}
	return strings.EqualFold(canonicalCwd(a), canonicalCwd(b))
}
