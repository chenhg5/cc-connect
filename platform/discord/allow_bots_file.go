package discord

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// maomao fork: allow_bots_file = "<path>" — one more allow list for bot / webhook
// authors, read at run time, merged with allow_bots.
//
// Why: role-household (the "板子" voice host) creates one webhook per Discord
// channel it relays the owner's speech into, and appends each new webhook ID to
// this file. Only those IDs are let through (review rh-voice-summary-router
// point 3: allow only the webhooks the host itself created, never "any webhook
// named 板子"), and adding one needs no config edit or restart.
//
// Format: one ID per line; blank lines and lines starting with # are skipped.
// The file is re-read when its mtime or size changes; a missing or unreadable
// file allows nobody (allow_bots still applies).
type allowBotsFile struct {
	path string

	mu    sync.Mutex
	mtime time.Time
	size  int64
	ids   map[string]bool
}

func newAllowBotsFile(path string) *allowBotsFile {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	return &allowBotsFile{path: path}
}

func (f *allowBotsFile) has(id string) bool {
	if f == nil || id == "" {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	st, err := os.Stat(f.path)
	if err != nil {
		f.ids, f.mtime, f.size = nil, time.Time{}, 0
		return false
	}
	if f.ids == nil || !st.ModTime().Equal(f.mtime) || st.Size() != f.size {
		data, err := os.ReadFile(f.path)
		if err != nil {
			f.ids = nil
			return false
		}
		ids := map[string]bool{}
		sc := bufio.NewScanner(bytes.NewReader(data))
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			ids[line] = true
		}
		f.ids, f.mtime, f.size = ids, st.ModTime(), st.Size()
	}
	return f.ids[id]
}

// botAllowed: a message from another bot (or a webhook) gets through when its
// author ID is in allow_bots or in allow_bots_file.
func (p *Platform) botAllowed(id string) bool {
	if p.allowBots != "" && core.AllowList(p.allowBots, id) {
		return true
	}
	return p.allowBotsFile.has(id)
}
