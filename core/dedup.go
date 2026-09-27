package core

import (
	"sync"
	"time"
)

// DefaultDedupTTL is the default dedup window used when a platform does not
// configure one explicitly. It is intentionally generous — long enough to
// absorb a network jitter / ACK-delay retry, short enough to keep the map
// bounded under steady traffic.
const DefaultDedupTTL = 60 * time.Second

// StartTime is set once at process startup.
// Platforms use it to discard messages created before the current process started,
// preventing replayed/unacknowledged messages from being re-processed after a restart.
var StartTime = time.Now()

// MessageDedup tracks recently seen message IDs to prevent duplicate processing.
// Safe for concurrent use.
//
// A zero-value MessageDedup uses DefaultDedupTTL; platforms can override via
// NewMessageDedup(ttl). The struct intentionally stays a value type so existing
// platform code that embeds `dedup core.MessageDedup` keeps working unchanged.
type MessageDedup struct {
	mu   sync.Mutex
	seen map[string]time.Time
	ttl  time.Duration // 0 means "use DefaultDedupTTL"
}

// NewMessageDedup returns a MessageDedup with a custom TTL. ttl <= 0 falls back
// to DefaultDedupTTL.
func NewMessageDedup(ttl time.Duration) *MessageDedup {
	if ttl <= 0 {
		ttl = DefaultDedupTTL
	}
	return &MessageDedup{ttl: ttl}
}

// TTL returns the dedup window in effect (handy for diagnostics and tests).
func (d *MessageDedup) TTL() time.Duration {
	if d == nil {
		return 0
	}
	if d.ttl <= 0 {
		return DefaultDedupTTL
	}
	return d.ttl
}

// IsDuplicate returns true if msgID was already seen within the TTL window.
// Empty msgID is never considered a duplicate.
func (d *MessageDedup) IsDuplicate(msgID string) bool {
	if d == nil || msgID == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.seen == nil {
		d.seen = make(map[string]time.Time)
	}
	now := time.Now()
	ttl := d.TTL()
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
