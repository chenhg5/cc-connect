package core

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type referenceKind string

const (
	referenceKindUnknown referenceKind = "unknown"
	referenceKindFile    referenceKind = "file"
	referenceKindDir     referenceKind = "dir"
)

type referenceLocationFormat string

const (
	referenceLocationNone         referenceLocationFormat = ""
	referenceLocationColonLine    referenceLocationFormat = "colon_line"
	referenceLocationColonLineCol referenceLocationFormat = "colon_line_col"
	referenceLocationColonRange   referenceLocationFormat = "colon_line_range"
	referenceLocationHashLine     referenceLocationFormat = "hash_line"
	referenceLocationHashLineCol  referenceLocationFormat = "hash_line_col"
)

type localReference struct {
	kind           referenceKind
	raw            string
	pathOriginal   string
	pathAbs        string
	pathRel        string
	isRelative     bool
	locationFormat referenceLocationFormat
	lineStart      int
	lineEnd        int
	column         int
}

var (
	reMarkdownLink   = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)((?::\d+(?::\d+)?|:\d+-\d+)?)?`)
	reHashLocation   = regexp.MustCompile(`^(.*?)(#L(\d+)(?:C(\d+))?)$`)
	reColonLineCol   = regexp.MustCompile(`^(.*):(\d+):(\d+)$`)
	reColonLineRange = regexp.MustCompile(`^(.*):(\d+)-(\d+)$`)
	reColonLineOnly  = regexp.MustCompile(`^(.*):(\d+)$`)
)

func parseUserLocalReference(raw, workspaceDir string) (*localReference, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty reference")
	}
	if match := reMarkdownLink.FindStringSubmatch(raw); len(match) >= 3 && match[0] == raw {
		suffix := ""
		if len(match) >= 4 {
			suffix = match[3]
		}
		raw = match[2] + suffix
	}
	ref, ok := parseLocalReference(raw, workspaceDir)
	if !ok {
		return nil, fmt.Errorf("cannot parse local reference")
	}
	return ref, nil
}

func parseLocalReference(raw, workspaceDir string) (*localReference, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || isWebURL(raw) || strings.HasPrefix(raw, "//") {
		return nil, false
	}
	ref := &localReference{raw: raw}
	pathPart := raw
	switch {
	case reHashLocation.MatchString(pathPart):
		m := reHashLocation.FindStringSubmatch(pathPart)
		pathPart = m[1]
		ref.lineStart = atoiSafe(m[3])
		ref.column = atoiSafe(m[4])
		if ref.column > 0 {
			ref.locationFormat = referenceLocationHashLineCol
		} else {
			ref.locationFormat = referenceLocationHashLine
		}
	case reColonLineCol.MatchString(pathPart):
		m := reColonLineCol.FindStringSubmatch(pathPart)
		pathPart = m[1]
		ref.lineStart = atoiSafe(m[2])
		ref.column = atoiSafe(m[3])
		ref.locationFormat = referenceLocationColonLineCol
	case reColonLineRange.MatchString(pathPart):
		m := reColonLineRange.FindStringSubmatch(pathPart)
		pathPart = m[1]
		ref.lineStart = atoiSafe(m[2])
		ref.lineEnd = atoiSafe(m[3])
		ref.locationFormat = referenceLocationColonRange
	case reColonLineOnly.MatchString(pathPart):
		m := reColonLineOnly.FindStringSubmatch(pathPart)
		pathPart = m[1]
		ref.lineStart = atoiSafe(m[2])
		ref.locationFormat = referenceLocationColonLine
	}
	if strings.HasPrefix(pathPart, "file://") {
		u, err := url.Parse(pathPart)
		if err != nil || u.Path == "" {
			return nil, false
		}
		pathPart = u.Path
	}
	if !looksLikeLocalPath(pathPart) {
		return nil, false
	}
	ref.pathOriginal = pathPart
	ref.isRelative = !filepath.IsAbs(pathPart)
	if ref.isRelative {
		if workspaceDir != "" {
			ref.pathAbs = filepath.Clean(filepath.Join(workspaceDir, pathPart))
			if rel, err := filepath.Rel(workspaceDir, ref.pathAbs); err == nil {
				ref.pathRel = filepath.ToSlash(rel)
			}
		}
	} else {
		ref.pathAbs = filepath.Clean(pathPart)
		if workspaceDir != "" {
			if rel, err := filepath.Rel(workspaceDir, ref.pathAbs); err == nil {
				ref.pathRel = filepath.ToSlash(rel)
			}
		}
	}
	ref.kind = inferReferenceKind(ref)
	return ref, true
}

func inferReferenceKind(ref *localReference) referenceKind {
	if ref == nil {
		return referenceKindUnknown
	}
	if ref.pathAbs != "" {
		if info, err := os.Stat(ref.pathAbs); err == nil {
			if info.IsDir() {
				return referenceKindDir
			}
			return referenceKindFile
		}
	}
	if ref.locationFormat != referenceLocationNone {
		return referenceKindFile
	}
	if strings.HasSuffix(ref.pathOriginal, "/") {
		return referenceKindDir
	}
	base := filepath.Base(strings.TrimSuffix(ref.pathOriginal, "/"))
	if filepath.Ext(base) != "" {
		return referenceKindFile
	}
	return referenceKindUnknown
}

func looksLikeLocalPath(path string) bool {
	if path == "" || strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") || strings.HasPrefix(path, "//") {
		return false
	}
	switch {
	case strings.HasPrefix(path, "/"):
		return true
	case strings.HasPrefix(path, "./"), strings.HasPrefix(path, "../"):
		return true
	case strings.Contains(path, "/"):
		return true
	default:
		base := filepath.Base(path)
		return strings.Contains(base, ".")
	}
}

func isWebURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func atoiSafe(s string) int {
	if s == "" {
		return 0
	}
	var n int
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}
