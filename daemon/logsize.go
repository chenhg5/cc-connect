package daemon

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseLogSize converts a human-friendly byte size string (e.g. "10MB",
// "512K", "1G", or a raw byte count) into a byte count. Suffixes are
// case-insensitive and may be followed by optional whitespace. Both the SI
// short forms (K, M, G) and the long forms (KB, MB, GB) are accepted and
// always use the binary (1024-based) multiplier — matching what users see
// in editor "file size" columns and the existing DefaultLogMaxSize comment
// of "10 MB".
//
// Returns an error if the input is empty, negative, has an unknown suffix,
// or cannot be parsed as an integer.
func ParseLogSize(s string) (int64, error) {
	orig := s
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("log size: empty value")
	}

	// Walk from the end of the string. Suffix is one of the documented forms
	// (K, KB, M, MB, G, GB, T, TB). Comparison is case-insensitive — the
	// long forms are tried first so e.g. "10MB" matches "MB" rather than
	// falling through to the bare "B" branch. The numeric part is parsed
	// verbatim, so a stray "10XYZ" fails loudly rather than silently
	// downgrading to a 10-byte log.
	upper := strings.ToUpper(s)
	var multiplier int64 = 1
	var numPart string
	switch {
	case strings.HasSuffix(upper, "TB"):
		multiplier = 1024 * 1024 * 1024 * 1024
		numPart = s[:len(s)-len("TB")]
	case strings.HasSuffix(upper, "T"):
		multiplier = 1024 * 1024 * 1024 * 1024
		numPart = s[:len(s)-len("T")]
	case strings.HasSuffix(upper, "GB"):
		multiplier = 1024 * 1024 * 1024
		numPart = s[:len(s)-len("GB")]
	case strings.HasSuffix(upper, "G"):
		multiplier = 1024 * 1024 * 1024
		numPart = s[:len(s)-len("G")]
	case strings.HasSuffix(upper, "MB"):
		multiplier = 1024 * 1024
		numPart = s[:len(s)-len("MB")]
	case strings.HasSuffix(upper, "M"):
		multiplier = 1024 * 1024
		numPart = s[:len(s)-len("M")]
	case strings.HasSuffix(upper, "KB"):
		multiplier = 1024
		numPart = s[:len(s)-len("KB")]
	case strings.HasSuffix(upper, "K"):
		multiplier = 1024
		numPart = s[:len(s)-len("K")]
	case strings.HasSuffix(upper, "B"):
		// Explicit bytes — only the bare "B" suffix (not "XB" for some X).
		// The "KB"/"MB"/"GB"/"TB" cases above are tried first and win.
		multiplier = 1
		numPart = s[:len(s)-len("B")]
	default:
		numPart = s
	}

	numPart = strings.TrimSpace(numPart)
	if numPart == "" {
		return 0, fmt.Errorf("log size %q: missing numeric part", orig)
	}

	n, err := strconv.ParseInt(numPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("log size %q: %w", orig, err)
	}
	if n < 0 {
		return 0, fmt.Errorf("log size %q: must be non-negative", orig)
	}

	return n * multiplier, nil
}
