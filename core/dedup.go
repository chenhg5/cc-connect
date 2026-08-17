package core

import (
	"sync"
	"time"
)

// dedupTTL is the default dedup window used when a zero-value MessageDedup is
// constructed (preserves the historical behavior of every platform-level
// MessageDedup embedded as `core.MessageDedup{}`).
const dedupTTL = 60 * time.Second

// StartTime is set once at process startup.
// Platforms use it to discard messages created before the current process started,
// preventing replayed/unacknowledged messages from being re-processed after a restart.
var StartTime = time.Now()

// MessageDedup tracks recently seen message IDs to prevent duplicate processing.
// Safe for concurrent use.
//
// The zero value uses a fixed 60s window (preserving the original behavior of
// every platform that embeds this as `core.MessageDedup{}`). Use
// NewMessageDedup to override the window — the engine uses this for the
// cross-platform safety net described in issue #1667, where a per-platform
// dedup key (e.g. WeChat's `from|msg_id|seq|create_time_ms|client_id`) misses
// server retransmissions whose create_time_ms gets refreshed on retry.
type MessageDedup struct {
	mu   sync.Mutex
	seen map[string]time.Time
	ttl  time.Duration // 0 = use dedupTTL default
}

// NewMessageDedup returns a MessageDedup with a caller-supplied window.
// Pass 0 or a negative value to get the package default (60s).
func NewMessageDedup(ttl time.Duration) *MessageDedup {
	if ttl <= 0 {
		ttl = dedupTTL
	}
	return &MessageDedup{ttl: ttl}
}

// IsDuplicate returns true if msgID was already seen within the TTL window.
// Empty msgID is never considered a duplicate.
func (d *MessageDedup) IsDuplicate(msgID string) bool {
	if msgID == "" {
		return false
	}
	ttl := d.ttl
	if ttl <= 0 {
		ttl = dedupTTL
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.seen == nil {
		d.seen = make(map[string]time.Time)
	}
	now := time.Now()
	for k, t := range d.seen {
		if now.Sub(t) > ttl {
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
