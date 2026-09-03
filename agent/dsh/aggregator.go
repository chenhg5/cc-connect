package dsh

import (
	"strings"
	"sync"
	"time"
)

// chunkAggregator coalesces tiny stream deltas (dsh relays provider output
// token by token) into engine-sized chunks. The cc-connect engine throttles
// per-EventText delivery (stream preview: 1.5s / 30 chars; rich cards:
// 200ms / 20 chars), so one EventText per token makes the platform message
// grow word by word. Aggregating to sentence-ish pieces restores the same
// delivery cadence other agents (Claude Code, Codex) produce.
type chunkAggregator struct {
	mu sync.Mutex

	minChars int           // flush once this many runes buffered
	maxWait  time.Duration // flush whatever is buffered after this long
	boundary func(r rune) bool

	buf   strings.Builder
	runes int
	timer *time.Timer
	emit  func(string)
}

// newChunkAggregator creates an aggregator that emits via emit.
// minChars<=0 disables the size threshold (flush on timer/boundary only);
// maxWait<=0 disables the timer (flush on size/boundary only).
func newChunkAggregator(minChars int, maxWait time.Duration, emit func(string)) *chunkAggregator {
	return &chunkAggregator{
		minChars: minChars,
		maxWait:  maxWait,
		boundary: sentenceBoundary,
		emit:     emit,
	}
}

// sentenceBoundary reports whether r ends a natural delivery unit.
func sentenceBoundary(r rune) bool {
	switch r {
	case '。', '！', '？', '?', '.', '!', '\n', '\r', '；', ';', '：', ':':
		return true
	default:
		return false
	}
}

// Append buffers one delta and flushes when the buffer is large enough,
// ends on a sentence boundary, or the max-wait timer fires.
func (a *chunkAggregator) Append(text string) {
	if text == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	a.buf.WriteString(text)
	a.runes += len([]rune(text))

	if a.runes >= a.minChars {
		a.cancelTimerLocked()
		a.flushLocked()
		return
	}
	last := []rune(text)[len([]rune(text))-1]
	if a.boundary(last) {
		a.cancelTimerLocked()
		a.flushLocked()
		return
	}
	if a.maxWait > 0 && a.timer == nil {
		a.timer = time.AfterFunc(a.maxWait, a.flushNow)
	}
}

// Flush emits any buffered text immediately (used at turn/tool boundaries
// and on close so no content is held back).
func (a *chunkAggregator) Flush() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cancelTimerLocked()
	a.flushLocked()
}

// flushNow is the timer callback (no lock held).
func (a *chunkAggregator) flushNow() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.timer = nil
	a.flushLocked()
}

// flushLocked emits and clears the buffer. Must hold a.mu.
func (a *chunkAggregator) flushLocked() {
	if a.runes == 0 {
		return
	}
	text := a.buf.String()
	a.buf.Reset()
	a.runes = 0
	a.emit(text)
}

// Cancel stops the pending timer (call on session close).
func (a *chunkAggregator) Cancel() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cancelTimerLocked()
}

func (a *chunkAggregator) cancelTimerLocked() {
	if a.timer != nil {
		a.timer.Stop()
		a.timer = nil
	}
}
