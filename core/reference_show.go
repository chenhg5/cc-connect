package core

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	defaultShowHeadLines    = 80
	defaultShowContextLines = 8
	defaultShowMaxRange     = 120
	defaultShowMaxEntries   = 50
)

type referenceViewMode string

const (
	referenceViewFileHead referenceViewMode = "file_head"
	referenceViewContext  referenceViewMode = "context"
	referenceViewRange    referenceViewMode = "range"
	referenceViewDir      referenceViewMode = "dir"
)

type referenceViewRequest struct {
	Ref        *localReference
	Mode       referenceViewMode
	Window     int
	MaxLines   int
	MaxEntries int
}

func buildReferenceViewRequest(rawRef, workspaceDir string) (*referenceViewRequest, error) {
	ref, err := parseUserLocalReference(rawRef, workspaceDir)
	if err != nil {
		return nil, err
	}
	if ref.kind == referenceKindDir && ref.locationFormat != referenceLocationNone {
		return nil, fmt.Errorf("directory reference cannot carry a location")
	}
	req := &referenceViewRequest{
		Ref:        ref,
		Window:     defaultShowContextLines,
		MaxLines:   defaultShowMaxRange,
		MaxEntries: defaultShowMaxEntries,
	}
	switch {
	case ref.kind == referenceKindDir:
		req.Mode = referenceViewDir
	case ref.locationFormat == referenceLocationColonRange:
		req.Mode = referenceViewRange
	case ref.locationFormat != referenceLocationNone:
		req.Mode = referenceViewContext
	default:
		req.Mode = referenceViewFileHead
		req.MaxLines = defaultShowHeadLines
	}
	return req, nil
}

func renderReferenceView(req *referenceViewRequest) (string, error) {
	if req == nil || req.Ref == nil {
		return "", fmt.Errorf("nil view request")
	}
	path := req.Ref.pathAbs
	if path == "" {
		path = req.Ref.pathOriginal
	}
	if path == "" {
		return "", fmt.Errorf("empty path")
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("path does not exist")
		}
		return "", err
	}
	if info.IsDir() {
		if req.Ref.locationFormat != referenceLocationNone {
			return "", fmt.Errorf("directory reference cannot carry a location")
		}
		return renderReferenceDir(path, req)
	}
	return renderReferenceFile(path, req)
}

func renderReferenceFile(path string, req *referenceViewRequest) (string, error) {
	var (
		lines     []string
		truncated bool
		err       error
		note      string
	)

	switch req.Mode {
	case referenceViewContext:
		lines, _, err = readFileContext(path, req.Ref.lineStart, req.Window, req.Window, req.MaxLines)
	case referenceViewRange:
		lines, truncated, err = readFileRange(path, req.Ref.lineStart, req.Ref.lineEnd, req.MaxLines)
		if truncated {
			note = fmt.Sprintf("Only showing the first %d lines of the requested range.", req.MaxLines)
		}
	default:
		lines, truncated, err = readFileHead(path, req.MaxLines)
		if truncated {
			note = fmt.Sprintf("Only showing the first %d lines.", req.MaxLines)
		}
	}
	if err != nil {
		return "", err
	}
	title := showReferenceTitle(req.Ref)
	if title == "" {
		title = cleanDisplayPath(path)
	}
	var sb strings.Builder
	sb.WriteString(title)
	if note != "" {
		sb.WriteString("\n")
		sb.WriteString(note)
	}
	sb.WriteString("\n```")
	if lang := codeFenceLanguage(path); lang != "" {
		sb.WriteString(lang)
	}
	sb.WriteString("\n")
	if len(lines) > 0 {
		sb.WriteString(strings.Join(lines, "\n"))
		sb.WriteString("\n")
	}
	sb.WriteString("```")
	return sb.String(), nil
}

func renderReferenceDir(path string, req *referenceViewRequest) (string, error) {
	entries, truncated, err := readDirEntries(path, req.MaxEntries)
	if err != nil {
		return "", err
	}
	title := showReferenceTitle(req.Ref)
	if title == "" {
		title = "📁 " + appendDirSuffix(cleanDisplayPath(path), referenceKindDir)
	}
	var sb strings.Builder
	sb.WriteString(title)
	if len(entries) == 0 {
		sb.WriteString("\n(empty)")
		return sb.String(), nil
	}
	sb.WriteString("\n")
	for _, entry := range entries {
		sb.WriteString("- ")
		sb.WriteString(entry)
		sb.WriteString("\n")
	}
	if truncated {
		sb.WriteString(fmt.Sprintf("\nOnly showing the first %d entries.", req.MaxEntries))
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

func showReferenceTitle(ref *localReference) string {
	if ref == nil {
		return ""
	}
	body := referenceDisplaySource(ref, "relative") + renderReferenceLocation(ref)
	return applyReferenceMarker("emoji", ref.kind, body)
}

func readFileHead(path string, maxLines int) ([]string, bool, error) {
	if maxLines <= 0 {
		maxLines = defaultShowHeadLines
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	lines := make([]string, 0, maxLines)
	truncated := false
	for scanner.Scan() {
		if len(lines) >= maxLines {
			truncated = true
			break
		}
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, false, err
	}
	return lines, truncated, nil
}

func readFileRange(path string, start, end, maxLines int) ([]string, bool, error) {
	if start <= 0 {
		return nil, false, fmt.Errorf("invalid start line")
	}
	if end <= 0 || end < start {
		return nil, false, fmt.Errorf("invalid end line")
	}
	if maxLines <= 0 {
		maxLines = defaultShowMaxRange
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	lines := make([]string, 0, minInt(end-start+1, maxLines))
	lineNo := 0
	truncated := false
	for scanner.Scan() {
		lineNo++
		if lineNo < start {
			continue
		}
		if lineNo > end {
			break
		}
		if len(lines) >= maxLines {
			truncated = true
			break
		}
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, false, err
	}
	return lines, truncated, nil
}

func readFileContext(path string, line, before, after, maxLines int) ([]string, bool, error) {
	if line <= 0 {
		return nil, false, fmt.Errorf("invalid line")
	}
	start := line - before
	if start < 1 {
		start = 1
	}
	end := line + after
	return readFileRange(path, start, end, maxLines)
}

func readDirEntries(path string, maxEntries int) ([]string, bool, error) {
	if maxEntries <= 0 {
		maxEntries = defaultShowMaxEntries
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, false, err
	}
	out := make([]string, 0, minInt(len(entries), maxEntries))
	truncated := false
	for i, entry := range entries {
		if i >= maxEntries {
			truncated = true
			break
		}
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		out = append(out, name)
	}
	return out, truncated, nil
}

func codeFenceLanguage(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".ts":
		return "ts"
	case ".tsx":
		return "tsx"
	case ".js":
		return "js"
	case ".jsx":
		return "jsx"
	case ".py":
		return "python"
	case ".md":
		return "markdown"
	case ".json":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".sh":
		return "bash"
	default:
		return ""
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
