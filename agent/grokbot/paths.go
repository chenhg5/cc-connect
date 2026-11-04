package grokbot

import (
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// pathDeniedError means a reply attachment resolved outside the directories
// Grok Bot is allowed to read back into the chat.
type pathDeniedError struct {
	Path string
}

func (e *pathDeniedError) Error() string {
	return "grokbot: reply path " + strconvQuote(e.Path) + " is outside work_dir, the attachments directory, the outbox, and reply_path_allow"
}

func strconvQuote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `'`) + `"`
}

// allowedRoots are the only directories a reply may read. work_dir covers the
// project and its .cc-connect/attachments tree; outbox_dir covers files the
// routine drops next to the reply; reply_path_allow adds explicit extras.
// Symlink ancestors are resolved so a link cannot widen the set.
func (a *Agent) allowedRoots() []string {
	a.mu.Lock()
	work := a.workDir
	extra := append([]string(nil), a.pathAllow...)
	outbox := ""
	if a.outboxEnabled {
		if a.explicitOutbox != "" {
			outbox = a.explicitOutbox
		} else {
			outbox = filepath.Join(work, ".cc-connect", "grokbot", "outbox")
		}
	}
	a.mu.Unlock()

	raw := []string{work, filepath.Join(work, ".cc-connect", "attachments")}
	if outbox != "" {
		raw = append(raw, outbox)
	}
	raw = append(raw, extra...)

	seen := make(map[string]struct{}, len(raw))
	var roots []string
	for _, r := range raw {
		resolved := resolveRoot(r)
		if resolved == "" {
			continue
		}
		if _, ok := seen[resolved]; ok {
			continue
		}
		seen[resolved] = struct{}{}
		roots = append(roots, resolved)
	}
	return roots
}

func resolveRoot(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return ""
	}
	return resolveExistingPrefix(filepath.Clean(abs))
}

// resolveExistingPrefix evals the longest existing ancestor so a symlink
// parent is not treated as a lexical path.
func resolveExistingPrefix(abs string) string {
	parent := abs
	var suffix []string
	for {
		if _, err := os.Lstat(parent); err == nil {
			if eval, err := filepath.EvalSymlinks(parent); err == nil {
				parent = eval
			}
			if len(suffix) == 0 {
				return parent
			}
			return filepath.Join(append([]string{parent}, suffix...)...)
		}
		next := filepath.Dir(parent)
		if next == parent {
			return abs
		}
		suffix = append([]string{filepath.Base(parent)}, suffix...)
		parent = next
	}
}

func pathAllowed(resolved string, roots []string) bool {
	resolved = filepath.Clean(resolved)
	for _, root := range roots {
		rel, err := filepath.Rel(root, resolved)
		if err != nil {
			continue
		}
		if rel == "." {
			return true
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		return true
	}
	return false
}

func readLocal(p string, roots []string) ([]byte, string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return nil, "", fmt.Errorf("grokbot: empty attachment path")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return nil, "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, "", err
	}
	if !pathAllowed(resolved, roots) {
		return nil, "", &pathDeniedError{Path: p}
	}
	st, err := os.Stat(resolved)
	if err != nil {
		return nil, "", err
	}
	if st.Size() > maxMediaBytes {
		return nil, "", fmt.Errorf("grokbot: %s exceeds size limit", filepath.Base(p))
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, "", err
	}
	return data, filepath.Base(p), nil
}

// mediaType uses the extension when it maps to a type, and sniffs the bytes
// otherwise. Unknown extensions must not be reported as image/png.
func mediaType(name string, data []byte) string {
	if ext := filepath.Ext(name); ext != "" {
		if mt := mime.TypeByExtension(ext); mt != "" {
			return mt
		}
	}
	if len(data) == 0 {
		return "application/octet-stream"
	}
	detected := http.DetectContentType(data)
	if i := strings.Index(detected, ";"); i >= 0 {
		detected = strings.TrimSpace(detected[:i])
	}
	if detected == "" {
		return "application/octet-stream"
	}
	return detected
}
