package core

import (
	"context"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// TaskCompletionSender lets a platform deliver a completed interactive turn
// with its original conversation context and explicitly linked output files.
type TaskCompletionSender interface {
	TaskCompletionWorkspace(replyCtx any) (string, bool)
	SendTaskCompletion(ctx context.Context, replyCtx any, text string, files []FileAttachment) error
}

const (
	maxCompletionFiles     = 3
	maxCompletionFileBytes = 10 << 20
)

var completionLink = regexp.MustCompile(`!?\[([^\]\n]+)\]\((<[^>\n]+>|[^\s)]+)\)`)

// completionFiles handles Markdown file links in the final answer. Only
// regular, visible workspace files created or updated during this turn are
// eligible. OpenRoot prevents symlink and directory traversal outside the root.
func completionFiles(text, workspace string, started time.Time, allowFiles bool) (string, []FileAttachment) {
	var root *os.Root
	if allowFiles && filepath.IsAbs(workspace) {
		var err error
		root, err = os.OpenRoot(workspace)
		if err != nil {
			slog.Warn("could not open task output workspace", "error", err)
		} else {
			defer root.Close()
		}
	}
	var files []FileAttachment
	seen := make(map[string]bool)
	text = completionLink.ReplaceAllStringFunc(text, func(link string) string {
		m := completionLink.FindStringSubmatch(link)
		dest := strings.Trim(m[2], "<>")
		if !strings.HasPrefix(dest, "/") && !strings.HasPrefix(dest, "file://") &&
			(!filepath.IsLocal(dest) || !strings.Contains(dest, ".") || strings.Contains(dest, ":")) {
			return link
		}
		if !strings.HasPrefix(dest, "file://") {
			if decoded, err := url.PathUnescape(dest); err == nil {
				dest = decoded
			}
		}
		ref, ok := parseLocalReference(dest, workspace)
		if !ok || !filepath.IsAbs(ref.pathAbs) {
			return link
		}
		rel, err := filepath.Rel(workspace, ref.pathAbs)
		if root != nil && err == nil && ref.locationFormat == referenceLocationNone && filepath.IsLocal(rel) && !seen[rel] && len(files) < maxCompletionFiles {
			seen[rel] = true
			if f, ok := readCompletionFile(root, rel, started); ok {
				files = append(files, f)
			}
		}
		// A local download link cannot be opened from a remote QQ client.
		return m[1] + " (" + filepath.Base(ref.pathAbs) + ")"
	})
	return text, files
}

func readCompletionFile(root *os.Root, rel string, started time.Time) (FileAttachment, bool) {
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if strings.HasPrefix(part, ".") {
			return FileAttachment{}, false
		}
	}
	info, err := root.Lstat(rel)
	if err != nil || !info.Mode().IsRegular() || info.ModTime().Before(started.Truncate(time.Second)) ||
		info.Size() > maxCompletionFileBytes {
		return FileAttachment{}, false
	}
	f, err := root.Open(rel)
	if err != nil {
		slog.Warn("could not open task output file", "error", err)
		return FileAttachment{}, false
	}
	defer f.Close()
	current, err := f.Stat()
	if err != nil || !os.SameFile(info, current) {
		return FileAttachment{}, false
	}
	data, err := io.ReadAll(io.LimitReader(f, maxCompletionFileBytes+1))
	if err != nil || len(data) > maxCompletionFileBytes {
		slog.Warn("could not read task output file", "error", err)
		return FileAttachment{}, false
	}
	return FileAttachment{FileName: filepath.Base(rel), Data: data}, true
}
