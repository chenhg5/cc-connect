package core

import (
	"sync"
	"time"
)

const dedupTTL = 60 * time.Second

// oldMessageGrace is the window before StartTime where messages are still
// accepted. After a daemon restart, Feishu re-delivers messages queued during
// the downtime with their original create_time, which may be minutes before
// StartTime. A 5-minute grace covers typical restart cycles without
// re-processing truly stale messages from hours ago.
const oldMessageGrace = 5 * time.Minute

// StartTime is set at process startup and updated when platforms reconnect.
// Platforms use it to discard messages created before the current process started,
// preventing replayed/unacknowledged messages from being re-processed after a restart.
// Call UpdateStartTime when a platform's real-time connection is established
// (e.g. WebSocket connected), so the cutoff reflects actual connectivity, not just
// process boot time — this avoids falsely dropping messages queued during brief
// restart windows.
var StartTime = time.Now()

// UpdateStartTime advances StartTime to the current time.
// Safe to call from any goroutine. Only moves the cutoff forward, never backward.
func UpdateStartTime() {
	now := time.Now()
	if now.After(StartTime) {
		StartTime = now
	}
}

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

// IsOldMessage returns true if msgTime is before StartTime minus the grace period.
// The grace period covers messages queued during a daemon restart that get
// re-delivered with their original (pre-restart) create_time.
func IsOldMessage(msgTime time.Time) bool {
	return msgTime.Before(StartTime.Add(-oldMessageGrace))
}
