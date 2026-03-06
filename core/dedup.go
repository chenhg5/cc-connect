package core

import (
	"sync"
	"time"
)

const dedupTTL = 60 * time.Second

// StartTime is set once at process startup.
// Platforms use it to discard messages created before the current process started,
// preventing replayed/unacknowledged messages from being re-processed after a restart.
var StartTime = time.Now()

// MessageDedup tracks recently seen message IDs to prevent duplicate processing.
// Safe for concurrent use.
type MessageDedup struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

// IsDuplicate returns true if msgID was already seen within the TTL window.
// Empty msgID is never considered a duplicate.
func (d *MessageDedup) IsDuplicate(msgID string) bool {
	if msgID == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.seen == nil {
		d.seen = make(map[string]time.Time)
	}
	now := time.Now()
	for k, t := range d.seen {
		if now.Sub(t) > dedupTTL {
			delete(d.seen, k)
		}
	}
	if _, ok := d.seen[msgID]; ok {
		return true
	}
	d.seen[msgID] = now
	return false
}

// IsOldMessage returns true if msgTime is before the process StartTime.
// A small grace period (2 seconds) is applied to avoid race conditions
// with messages sent right at startup.
func IsOldMessage(msgTime time.Time) bool {
	return msgTime.Before(StartTime.Add(-2 * time.Second))
}
