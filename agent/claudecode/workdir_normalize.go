package claudecode

import (
	"path/filepath"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// normalizeWorkDir canonicalizes a Claude Code working directory path so that
// the same logical directory always produces the same ~/.claude/projects/<key>
// directory name, regardless of how the user typed it. Without this, cc-connect
// and the desktop Claude Code CLI can encode the same directory into two
// different project directories, making sessions invisible to /resume on the
// other side (issue #1040).
//
// Normalization rules:
//  1. filepath.Clean collapses ".", "..", and trailing path separators
//     (per-platform: backslash on Windows, forward slash on Unix).
//  2. Windows drive letters are upper-cased (e.g. "d:\\foo" → "D:\\foo") so
//     that "d:\\foo" and "D:\\foo" encode to the same project key. Windows
//     filesystems are case-insensitive at the volume level, so this matches
//     what the desktop CLI does.
//  3. Unicode is normalized to NFC. macOS HFS+ historically stored paths in
//     NFD; APFS and most Linux filesystems use NFC. Without canonicalization,
//     a user-typed NFD path produces a different encoded key than an
//     NFC-typed path even when they refer to the same directory.
func normalizeWorkDir(path string) string {
	if path == "" {
		return path
	}
	cleaned := filepath.Clean(path)
	cleaned = strings.TrimRight(cleaned, `/\`)
	if cleaned == "" {
		// filepath.Clean on "/" or "\" returns the separator itself; if we
		// trimmed the only character, restore the root marker.
		cleaned = string(filepath.Separator)
	}
	cleaned = normalizeWindowsDriveLetter(cleaned)
	cleaned = norm.NFC.String(cleaned)
	return cleaned
}

// normalizeWindowsDriveLetter uppercases the leading single-letter volume
// specifier on Windows-style absolute paths (e.g. "d:\\foo" → "D:\\foo").
// We do this on every platform so that Linux-side encodings match what
// desktop Claude Code produces on Windows. The function is a no-op for
// paths that don't look like a drive-letter-rooted Windows path.
func normalizeWindowsDriveLetter(path string) string {
	if len(path) < 2 || path[1] != ':' {
		return path
	}
	c := path[0]
	if c < 'A' || (c > 'Z' && c < 'a') || c > 'z' {
		return path
	}
	if c >= 'a' && c <= 'z' {
		return string(c-('a'-'A')) + path[1:]
	}
	return path
}
