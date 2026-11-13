package feishu

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"log/slog"

	"github.com/chenhg5/cc-connect/core"
)

const (
	threadSeenMax         = 1000
	threadSeenWarnAt      = threadSeenMax * 8 / 10
	threadSeenSaveDelay   = 30 * time.Second
	threadSeenSaveRefresh = time.Hour
	threadStateVersion    = 1
)

type threadState struct {
	Version int                  `json:"version"`
	Threads map[string]time.Time `json:"threads"`
}

// threadSeenStatePath returns an empty path when no data dir is configured,
// which keeps tests and embedded use memory-only.
func threadSeenStatePath(platformName string, opts map[string]any) string {
	dataDir, _ := opts["cc_data_dir"].(string)
	if strings.TrimSpace(dataDir) == "" {
		return ""
	}
	project, _ := opts["cc_project"].(string)
	if strings.TrimSpace(project) == "" {
		project = "default"
	}
	name := "feishu-thread-state-" + sanitizeStatePart(platformName) + "-" + sanitizeStatePart(project) + ".json"
	return filepath.Join(dataDir, name)
}

func sanitizeStatePart(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "default"
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '_'
	}, s)
	// A path part must not be "." or "..".
	if s == "." || s == ".." {
		return "default"
	}
	return s
}

func (p *Platform) loadThreadSeen() {
	if p.threadStatePath == "" {
		return
	}
	raw, err := os.ReadFile(p.threadStatePath)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		slog.Warn(p.tag()+": thread state load failed", "path", p.threadStatePath, "error", err)
		return
	}
	var state threadState
	if err := json.Unmarshal(raw, &state); err != nil {
		slog.Warn(p.tag()+": thread state is corrupt, starting empty", "path", p.threadStatePath, "error", err)
		return
	}
	now := time.Now()
	seen := make(map[string]time.Time, len(state.Threads))
	expired, future := 0, 0
	for id, at := range state.Threads {
		if id == "" {
			continue
		}
		if at.After(now.Add(time.Minute)) {
			future++
			continue
		}
		if now.Sub(at) >= threadSeenTTL {
			expired++
			continue
		}
		seen[id] = at
	}
	evicted := pruneThreadSeen(seen, now)
	saved := make(map[string]time.Time, len(seen))
	for id, at := range seen {
		saved[id] = at
	}
	warnCapacity := len(seen) >= threadSeenWarnAt
	p.threadSeenMu.Lock()
	p.threadSeen = seen
	p.threadSeenSaved = saved
	p.threadSeenWarned = warnCapacity
	p.threadSeenMu.Unlock()
	slog.Info(p.tag()+": thread state loaded", "path", p.threadStatePath, "threads", len(seen))
	if expired > 0 {
		slog.Info(p.tag()+": thread state ignored expired entries", "count", expired, "remaining", len(seen))
	}
	if future > 0 {
		slog.Warn(p.tag()+": thread state contains future timestamps, ignored", "count", future, "path", p.threadStatePath)
	}
	if evicted > 0 {
		slog.Warn(p.tag()+": thread state at capacity on load, evicted oldest entries",
			"evicted", evicted, "remaining", len(seen), "max", threadSeenMax)
	}
	if warnCapacity {
		slog.Warn(p.tag()+": thread state nearing capacity",
			"threads", len(seen), "threshold", threadSeenWarnAt, "max", threadSeenMax)
	}
}

func (p *Platform) scheduleThreadSeenSave() {
	if p.threadStatePath == "" {
		return
	}
	p.threadSeenMu.Lock()
	if p.threadSaveTimer != nil {
		p.threadSeenMu.Unlock()
		return
	}
	var timer *time.Timer
	timer = time.AfterFunc(threadSeenSaveDelay, func() {
		p.threadSeenMu.Lock()
		if p.threadSaveTimer != timer {
			p.threadSeenMu.Unlock()
			return
		}
		p.threadSaveTimer = nil
		p.threadSeenMu.Unlock()
		p.persistThreadSeen()
	})
	p.threadSaveTimer = timer
	p.threadSeenMu.Unlock()
}

func (p *Platform) flushThreadSeen() {
	if p.threadStatePath == "" {
		return
	}
	p.threadSeenMu.Lock()
	if p.threadSaveTimer != nil {
		p.threadSaveTimer.Stop()
		p.threadSaveTimer = nil
	}
	p.threadSeenMu.Unlock()
	p.persistThreadSeen()
}

func (p *Platform) persistThreadSeen() {
	path := p.threadStatePath
	if path == "" {
		return
	}
	p.threadSaveMu.Lock()
	defer p.threadSaveMu.Unlock()

	p.threadSeenMu.Lock()
	state := threadState{
		Version: threadStateVersion,
		Threads: make(map[string]time.Time, len(p.threadSeen)),
	}
	for id, at := range p.threadSeen {
		if id == "" || time.Since(at) >= threadSeenTTL {
			continue
		}
		state.Threads[id] = at
	}
	p.threadSeenMu.Unlock()

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		slog.Warn(p.tag()+": thread state marshal failed", "error", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		slog.Warn(p.tag()+": thread state mkdir failed", "path", path, "error", err)
		return
	}
	if err := core.AtomicWriteFile(path, data, 0o644); err != nil {
		slog.Warn(p.tag()+": thread state save failed", "path", path, "error", err)
		return
	}
	p.threadSeenMu.Lock()
	if p.threadSeenSaved == nil {
		p.threadSeenSaved = make(map[string]time.Time, len(state.Threads))
	}
	for id := range p.threadSeenSaved {
		if _, ok := state.Threads[id]; !ok {
			delete(p.threadSeenSaved, id)
		}
	}
	for id, at := range state.Threads {
		p.threadSeenSaved[id] = at
	}
	p.threadSeenMu.Unlock()
}

// pruneThreadSeen removes expired entries and caps the map. When over the cap
// it evicts the oldest entries first.
func pruneThreadSeen(seen map[string]time.Time, now time.Time) int {
	for id, at := range seen {
		if id == "" || now.Sub(at) >= threadSeenTTL {
			delete(seen, id)
		}
	}
	if len(seen) <= threadSeenMax {
		return 0
	}
	type entry struct {
		id string
		at time.Time
	}
	entries := make([]entry, 0, len(seen))
	for id, at := range seen {
		entries = append(entries, entry{id: id, at: at})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].at.Before(entries[j].at) })
	evicted := len(entries) - threadSeenMax
	for _, e := range entries[:evicted] {
		delete(seen, e.id)
	}
	return evicted
}
