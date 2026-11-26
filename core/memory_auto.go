package core

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

const (
	memoryContentMaxRunes  = 2000
	autoMemoryMaxReadBytes = 256 * 1024
	autoMemoryListLimit    = 20
)

var errInvalidAutoMemoryName = errors.New("invalid auto memory filename")

type autoMemoryEntry struct {
	Name           string
	Lines          int
	LinesTruncated bool
}

type autoMemoryListing struct {
	Entries      []autoMemoryEntry
	Total        int
	IndexPreview string
}

func truncateMemoryRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "\n\n... (truncated)"
	}
	return s
}

func readBoundedFile(path string, maxBytes int64) (string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = f.Close() }()

	return readBoundedReader(f, maxBytes)
}

func readBoundedReader(r io.Reader, maxBytes int64) (string, bool, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return "", false, err
	}
	if int64(len(b)) <= maxBytes {
		return string(b), false, nil
	}
	// A byte limit can split the last UTF-8 character.
	return string(bytes.ToValidUTF8(b[:maxBytes], nil)), true, nil
}

func countMemoryLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(strings.TrimSuffix(s, "\n"), "\n") + 1
}

// Read one lookahead byte to distinguish an exact fit from a truncated count.
func countBoundedLines(r io.Reader, maxBytes int) (int, bool, error) {
	var buf [4096]byte
	lines := 0
	partial := false
	remaining := maxBytes
	for {
		n, err := r.Read(buf[:min(len(buf), remaining+1)])
		if err != nil && err != io.EOF {
			return 0, false, err
		}
		used := min(n, remaining)
		if used > 0 {
			lines += bytes.Count(buf[:used], []byte{'\n'})
			partial = buf[used-1] != '\n'
		}
		truncated := n > remaining
		if truncated || err == io.EOF {
			if partial {
				lines++
			}
			return lines, truncated, nil
		}
		remaining -= used
	}
}

func readAutoMemoryDir(root *os.Root) ([]os.DirEntry, error) {
	info, err := root.Stat(".")
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("auto memory path is not a directory")
	}
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	return dir.ReadDir(-1)
}

func visibleMarkdownName(name string) bool {
	return !strings.HasPrefix(name, ".") && strings.EqualFold(filepath.Ext(name), ".md")
}

// Root rejects escaped links with a non-errno error. Preserve genuine OS
// errors (e.g. permission or I/O failures) for the caller to report.
func autoMemoryLinkError(err error) error {
	var errno syscall.Errno
	if os.IsNotExist(err) || errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENOTDIR) || !errors.As(err, &errno) {
		return errInvalidAutoMemoryName
	}
	return err
}

func openAutoMemoryEntry(root *os.Root, name string) (*os.File, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	isLink := info.Mode()&os.ModeSymlink != 0
	path := name
	// Check every link's target name, including a visible alias of another
	// alias. Keep all path lookups relative to the same root handle.
	for hops := 0; info.Mode()&os.ModeSymlink != 0; hops++ {
		if hops >= 40 {
			return nil, errInvalidAutoMemoryName
		}
		target, err := root.Readlink(path)
		if err != nil {
			return nil, autoMemoryLinkError(err)
		}
		if filepath.IsAbs(target) || filepath.VolumeName(target) != "" || !visibleMarkdownName(filepath.Base(target)) {
			return nil, errInvalidAutoMemoryName
		}
		parent, _ := filepath.Split(path)
		path = parent + target // retain ".." for Root to resolve across directory links
		info, err = root.Lstat(path)
		if err != nil {
			return nil, autoMemoryLinkError(err)
		}
	}
	if !info.Mode().IsRegular() {
		return nil, errInvalidAutoMemoryName
	}
	f, err := root.Open(name)
	if err != nil {
		if isLink {
			return nil, autoMemoryLinkError(err)
		}
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		_ = f.Close()
		return nil, errInvalidAutoMemoryName
	}
	return f, nil
}

// Count every readable Markdown file, but read bytes only for displayed entries.
func (listing *autoMemoryListing) inspect(root *os.Root, name string, includeIndexPreview bool) error {
	f, err := openAutoMemoryEntry(root, name)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if len(listing.Entries) < autoMemoryListLimit {
		entry := autoMemoryEntry{Name: name}
		if includeIndexPreview && len(listing.Entries) == 0 && strings.EqualFold(name, "MEMORY.md") {
			listing.IndexPreview, entry.LinesTruncated, err = readBoundedReader(f, autoMemoryMaxReadBytes)
			entry.Lines = countMemoryLines(listing.IndexPreview)
		} else {
			entry.Lines, entry.LinesTruncated, err = countBoundedLines(f, autoMemoryMaxReadBytes)
		}
		if err != nil {
			return err
		}
		listing.Entries = append(listing.Entries, entry)
	}
	listing.Total++
	return nil
}

func listAutoMemoryFiles(dir string, includeIndexPreview bool) (autoMemoryListing, error) {
	var listing autoMemoryListing
	if dir == "" {
		return listing, nil
	}
	root, err := os.OpenRoot(dir)
	if os.IsNotExist(err) {
		return listing, nil
	}
	if err != nil {
		return listing, err
	}
	defer func() { _ = root.Close() }()
	entries, err := readAutoMemoryDir(root)
	if os.IsNotExist(err) {
		return listing, nil
	}
	if err != nil {
		return listing, err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && visibleMarkdownName(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := names[i], names[j]
		indexA, indexB := strings.EqualFold(a, "MEMORY.md"), strings.EqualFold(b, "MEMORY.md")
		if indexA != indexB {
			return indexA
		}
		lowerA, lowerB := strings.ToLower(a), strings.ToLower(b)
		if lowerA == lowerB {
			return a < b
		}
		return lowerA < lowerB
	})

	var firstErr error
	for _, name := range names {
		err := listing.inspect(root, name, includeIndexPreview)
		if err != nil && !os.IsNotExist(err) && !errors.Is(err, errInvalidAutoMemoryName) {
			slog.Warn("memory: failed to inspect auto memory entry", "error", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if listing.Total == 0 {
		return listing, firstErr
	}
	return listing, nil
}

// Return the already validated, open descriptor. The caller owns it.
func resolveAutoMemoryFile(dir, requested string) (string, *os.File, error) {
	name := strings.TrimSpace(requested)
	if name == "" || strings.ContainsAny(name, "/\\\x00") || strings.HasPrefix(name, ".") {
		return "", nil, errInvalidAutoMemoryName
	}
	if dir == "" {
		return "", nil, os.ErrNotExist
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = root.Close() }()
	entries, err := readAutoMemoryDir(root)
	if err != nil {
		return "", nil, err
	}

	ext := filepath.Ext(name)
	wanted := name
	if !strings.EqualFold(ext, ".md") {
		wanted += ".md"
	}
	matched := ""
	directoryMatch := false
	for _, entry := range entries {
		if !strings.EqualFold(entry.Name(), wanted) {
			continue
		}
		if entry.IsDir() {
			directoryMatch = true
			continue
		}
		if entry.Name() == wanted {
			matched = wanted
			break
		}
		if matched == "" {
			matched = entry.Name()
		}
	}
	if matched == "" {
		if directoryMatch || (ext != "" && !strings.EqualFold(ext, ".md")) {
			return "", nil, errInvalidAutoMemoryName
		}
		return "", nil, os.ErrNotExist
	}
	f, err := openAutoMemoryEntry(root, matched)
	return matched, f, err
}

func safeAutoMemoryError(err error) string {
	slog.Warn("memory: auto memory read failed", "error", err)
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

func (e *Engine) memoryUsage(hasMem, hasAuto bool) string {
	usage := e.i18n.T(MsgMemoryAddUsage)
	if !hasAuto {
		return usage
	}
	if !hasMem {
		usage, _, _ = strings.Cut(usage, "\n")
	}
	return usage + "\n" + e.i18n.T(MsgMemoryAutoUsage)
}

func (e *Engine) formatAutoMemoryList(listing autoMemoryListing) string {
	var out strings.Builder
	out.WriteString(e.i18n.T(MsgMemoryAutoHeader))
	for _, file := range listing.Entries {
		count := strconv.Itoa(file.Lines)
		if file.LinesTruncated {
			count += "+"
		}
		fmt.Fprintf(&out, "\n- %s (%s)", file.Name, e.i18n.Tf(MsgMemoryAutoLineCount, count))
	}
	if extra := listing.Total - len(listing.Entries); extra > 0 {
		out.WriteString("\n" + e.i18n.Tf(MsgMemoryAutoMoreFiles, extra))
	}
	return out.String()
}

func (e *Engine) showAutoMemoryOverview(p Platform, msg *Message, dir string) {
	listing, err := listAutoMemoryFiles(dir, true)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgMemoryAutoReadFailed, safeAutoMemoryError(err)))
		return
	}
	if listing.Total == 0 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgMemoryAutoEmpty))
		return
	}
	out := e.formatAutoMemoryList(listing)
	if strings.TrimSpace(listing.IndexPreview) != "" {
		out += "\n\n`" + listing.Entries[0].Name + "`\n\n" + truncateMemoryRunes(listing.IndexPreview, memoryContentMaxRunes)
	}
	e.reply(p, msg.ReplyCtx, out+"\n\n"+e.i18n.T(MsgMemoryAutoHint))
}

func (e *Engine) showAutoMemoryFile(p Platform, msg *Message, dir, requested string) {
	name, f, err := resolveAutoMemoryFile(dir, requested)
	if err != nil {
		switch {
		case errors.Is(err, errInvalidAutoMemoryName):
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgMemoryAutoInvalidFile))
		case os.IsNotExist(err):
			e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgMemoryAutoFileNotFound, requested))
		default:
			e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgMemoryAutoReadFailed, safeAutoMemoryError(err)))
		}
		return
	}
	defer func() { _ = f.Close() }()
	data, _, err := readBoundedReader(f, autoMemoryMaxReadBytes)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgMemoryAutoReadFailed, safeAutoMemoryError(err)))
		return
	}
	if strings.TrimSpace(data) == "" {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgMemoryEmpty, name))
		return
	}
	e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgMemoryAutoShowFile, name, truncateMemoryRunes(data, memoryContentMaxRunes)))
}
