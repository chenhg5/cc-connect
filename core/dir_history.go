package core

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

const (
	defaultDirHistorySize = 10
	dirHistoryFileName    = "dir_history.json"
)

// DirHistory manages directory switch history per project.
type DirHistory struct {
	mu        sync.RWMutex
	storePath string
	entries   map[string][]string // project name -> dir list (most recent first)
	maxSize   int
}

// NewDirHistory creates a new DirHistory with the given data directory.
func NewDirHistory(dataDir string) *DirHistory {
	dh := &DirHistory{
		storePath: filepath.Join(dataDir, dirHistoryFileName),
		entries:   make(map[string][]string),
		maxSize:   defaultDirHistorySize,
	}
	dh.load()
	return dh
}

// Add adds a directory to the history for the given project.
// If the directory already exists, it's moved to the front.
func (dh *DirHistory) Add(project, dir string) {
	if dir == "" {
		return
	}

	dh.mu.Lock()
	defer dh.mu.Unlock()

	entries := dh.entries[project]

	// Remove if exists
	for i, d := range entries {
		if d == dir {
			entries = append(entries[:i], entries[i+1:]...)
			break
		}
	}

	// Add to front
	entries = append([]string{dir}, entries...)

	// Trim to max size
	if len(entries) > dh.maxSize {
		entries = entries[:dh.maxSize]
	}

	dh.entries[project] = entries
	dh.saveLocked()
}

// List returns the history for the given project.
func (dh *DirHistory) List(project string) []string {
	dh.mu.RLock()
	defer dh.mu.RUnlock()

	entries := dh.entries[project]
	if entries == nil {
		return nil
	}

	// Return a copy
	result := make([]string, len(entries))
	copy(result, entries)
	return result
}

// Get returns the directory at the given index (1-based) for the project.
// Returns empty string if index is out of range.
func (dh *DirHistory) Get(project string, index int) string {
	dh.mu.RLock()
	defer dh.mu.RUnlock()

	entries := dh.entries[project]
	if index < 1 || index > len(entries) {
		return ""
	}
	return entries[index-1]
}

// Previous returns the previous directory (index 2, since index 1 is current).
func (dh *DirHistory) Previous(project string) string {
	return dh.Get(project, 2)
}

// Contains checks if a directory is in the history for the given project.
func (dh *DirHistory) Contains(project, dir string) bool {
	dh.mu.RLock()
	defer dh.mu.RUnlock()

	entries := dh.entries[project]
	for _, d := range entries {
		if d == dir {
			return true
		}
	}
	return false
}

// SetMaxSize sets the maximum history size.
func (dh *DirHistory) SetMaxSize(size int) {
	if size < 1 {
		size = 1
	}
	dh.mu.Lock()
	defer dh.mu.Unlock()
	dh.maxSize = size
}

func (dh *DirHistory) load() {
	if dh.storePath == "" {
		return
	}

	data, err := os.ReadFile(dh.storePath)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Error("dir_history: failed to read", "path", dh.storePath, "error", err)
		}
		return
	}

	var entries map[string][]string
	if err := json.Unmarshal(data, &entries); err != nil {
		slog.Error("dir_history: failed to unmarshal", "path", dh.storePath, "error", err)
		return
	}

	if entries != nil {
		dh.entries = entries
	}
}

func (dh *DirHistory) saveLocked() {
	if dh.storePath == "" {
		return
	}

	data, err := json.MarshalIndent(dh.entries, "", "  ")
	if err != nil {
		slog.Error("dir_history: failed to marshal", "error", err)
		return
	}

	if err := os.MkdirAll(filepath.Dir(dh.storePath), 0755); err != nil {
		slog.Error("dir_history: failed to create dir", "error", err)
		return
	}

	if err := AtomicWriteFile(dh.storePath, data, 0644); err != nil {
		slog.Error("dir_history: failed to write", "path", dh.storePath, "error", err)
	}
}