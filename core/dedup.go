package core

import (
	"sync"
	"time"
)

const dedupTTL = 60 * time.Second

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
