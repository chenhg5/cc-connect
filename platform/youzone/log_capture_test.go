package youzone

import (
	"context"
	"log/slog"
	"sync"
	"testing"
)

// logCapture is a fork-only test helper: a slog.Handler that retains every
// record in memory so tests can do field-level assertions on what the
// platform/youzone code logged. Kept in this _test.go file (not in core/) so
// the helper does not leak into upstream-aligned packages.
type logCapture struct {
	mu      sync.Mutex
	records []slog.Record
}

func (c *logCapture) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (c *logCapture) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, r.Clone())
	return nil
}

func (c *logCapture) WithAttrs(_ []slog.Attr) slog.Handler { return c }
func (c *logCapture) WithGroup(_ string) slog.Handler      { return c }

// snapshot returns the records collected so far. Safe to call after the code
// under test has finished.
func (c *logCapture) snapshot() []slog.Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]slog.Record, len(c.records))
	copy(out, c.records)
	return out
}

// findByMessage returns the first captured record whose Message matches msg.
// Returns the zero Record and false if no such record was logged.
func (c *logCapture) findByMessage(msg string) (slog.Record, bool) {
	for _, r := range c.snapshot() {
		if r.Message == msg {
			return r, true
		}
	}
	return slog.Record{}, false
}

// attrs flattens a record's attributes into map[string]any for ergonomic
// field-level assertions in tests.
func attrs(r slog.Record) map[string]any {
	out := make(map[string]any, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		out[a.Key] = a.Value.Any()
		return true
	})
	return out
}

// captureLogs swaps slog.Default() for the duration of fn so the test can
// inspect every record fn produced. The original default is restored before
// captureLogs returns.
func captureLogs(t *testing.T, fn func()) *logCapture {
	t.Helper()
	cap := &logCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(cap))
	defer slog.SetDefault(prev)
	fn()
	return cap
}
