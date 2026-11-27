package core

import (
	"log/slog"
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
//
// It is a wall-clock value, so it is only meaningful while the wall clock has
// not been stepped since startup. Comparisons against message timestamps should
// go through processStart, which corrects for wall-clock steps.
var StartTime = time.Now()

// startMono keeps the monotonic reading captured at startup. A wall-clock step
// (NTP correcting an RTC that came up hours off, for example) does not move the
// monotonic clock, so the elapsed process lifetime stays accurate.
var startMono = StartTime

var clockJumpWarnOnce sync.Once

// processStart returns the current wall-clock estimate of when this process
// started, derived from the monotonic clock. When the wall clock is stable this
// is StartTime; after a wall-clock step it stays the same instant in real time
// instead of pointing at the clock value captured at startup.
func processStart() time.Time {
	elapsed := time.Since(startMono) // monotonic-backed
	if elapsed < 0 {
		elapsed = 0
	}
	return time.Now().Add(-elapsed)
}

// startupCutoff returns the instant before which a message is considered to
// predate this process. It is the earlier of the recorded StartTime and the
// monotonic-clock estimate of the real start:
//   - normally both point at the same instant;
//   - when the host clock was ahead at startup and NTP later stepped it back,
//     StartTime sits in the future, so the monotonic estimate wins and fresh
//     messages are not mistaken for replays;
//   - when StartTime is explicitly pinned into the past (tests, or a caller
//     simulating a restart), that value wins, keeping the StartTime contract.
func startupCutoff() time.Time {
	cutoff := StartTime
	if ps := processStart(); ps.Before(cutoff) {
		cutoff = ps
	}
	return cutoff
}

func warnClockStepped() {
	clockJumpWarnOnce.Do(func() {
		if ahead := StartTime.Sub(processStart()); ahead > time.Minute {
			slog.Warn("host clock was ahead at startup and has since been corrected; using the monotonic process start for the old-message cutoff",
				"startup_wall_clock", StartTime.Format(time.RFC3339),
				"ahead_of_real_start", ahead.Round(time.Second).String())
		}
	})
}

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
//
// The cutoff comes from startupCutoff, which corrects the recorded StartTime
// with the monotonic clock. If the RTC comes up hours in the future and NTP
// later steps the clock back, a raw StartTime would sit in the future forever
// and every fresh message would be classified "old" and silently dropped (the
// bot looks connected but never answers). Server timestamps are real wall-clock
// instants, so comparing them against the corrected start keeps the startup
// replay filter right across such a step.
func IsOldMessage(msgTime time.Time) bool {
	warnClockStepped()
	return msgTime.Before(startupCutoff().Add(-2 * time.Second))
}
