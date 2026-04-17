package core

import (
	"log/slog"
	"path/filepath"
)

// NormalizeDirPath cleans a directory-like path and resolves symlinks when
// possible. If symlink resolution fails, it falls back to the cleaned path.
func NormalizeDirPath(path string) string {
	cleaned := filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return cleaned
	}
	if resolved != path {
		slog.Debug("directory path normalized", "original", path, "normalized", resolved)
	}
	return resolved
}
