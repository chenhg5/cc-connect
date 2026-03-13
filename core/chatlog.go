package core

import (
	"sync"
	"time"
)

// ChatLogEntry represents a single group chat message stored for summarization.
type ChatLogEntry struct {
	UserID    string
	UserName  string
	Content   string
	Timestamp time.Time
}

// ChatLog stores group chat messages in per-chat ring buffers (in-memory only).
type ChatLog struct {
	mu      sync.RWMutex
	logs    map[string][]ChatLogEntry // chatKey → entries
	maxSize int                       // max entries per chat
}

// NewChatLog creates a ChatLog with the given per-chat capacity.
func NewChatLog(maxSize int) *ChatLog {
	if maxSize <= 0 {
		maxSize = 500
	}
	return &ChatLog{
		logs:    make(map[string][]ChatLogEntry),
		maxSize: maxSize,
	}
}

// Record appends an entry to the chat's log, evicting the oldest if full.
func (cl *ChatLog) Record(chatKey string, entry ChatLogEntry) {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	entries := cl.logs[chatKey]
	if len(entries) >= cl.maxSize {
		// Drop oldest entries to make room
		drop := len(entries) - cl.maxSize + 1
		entries = entries[drop:]
	}
	cl.logs[chatKey] = append(entries, entry)
}

// GetRecent returns the last n entries for the given chat.
func (cl *ChatLog) GetRecent(chatKey string, n int) []ChatLogEntry {
	cl.mu.RLock()
	defer cl.mu.RUnlock()

	entries := cl.logs[chatKey]
	if n <= 0 || n >= len(entries) {
		result := make([]ChatLogEntry, len(entries))
		copy(result, entries)
		return result
	}
	result := make([]ChatLogEntry, n)
	copy(result, entries[len(entries)-n:])
	return result
}

// Clear removes all entries for the given chat.
func (cl *ChatLog) Clear(chatKey string) {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	delete(cl.logs, chatKey)
}

// GetSince returns all entries for the given chat since the specified time.
func (cl *ChatLog) GetSince(chatKey string, since time.Time) []ChatLogEntry {
	cl.mu.RLock()
	defer cl.mu.RUnlock()

	entries := cl.logs[chatKey]
	var result []ChatLogEntry
	for _, e := range entries {
		if !e.Timestamp.Before(since) {
			result = append(result, e)
		}
	}
	return result
}
