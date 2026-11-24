package core

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// outbox serialises outbound platform calls per session on its own goroutine.
// The turn loop only enqueues, so a slow or hung HTTP call can never keep the
// session lock and block later messages.
type outbox struct {
	ch       chan func()
	stop     chan struct{}
	once     sync.Once
	lastUsed atomic.Int64 // UnixNano; refreshed on every push

	// mu guards closed so a push that races with close() can never enqueue
	// into a channel nobody reads any more.
	mu     sync.RWMutex
	closed bool
}

const outboxQueueSize = 128

// outboxIdleTimeout is a variable so tests can shorten the janitor window.
var outboxIdleTimeout = 10 * time.Minute

func newOutbox() *outbox {
	o := &outbox{
		ch:   make(chan func(), outboxQueueSize),
		stop: make(chan struct{}),
	}
	o.lastUsed.Store(time.Now().UnixNano())
	go func() {
		for {
			select {
			case fn := <-o.ch:
				runOutboxTask(fn)
			case <-o.stop:
				// Drain anything already queued, then exit.
				for {
					select {
					case fn := <-o.ch:
						runOutboxTask(fn)
					default:
						return
					}
				}
			}
		}
	}()
	return o
}

func runOutboxTask(fn func()) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("outbox task panicked", "panic", r)
		}
	}()
	fn()
}

// close retires the outbox goroutine after the session has been idle for a
// while. Any task still queued is drained before the goroutine exits; a push
// racing with close runs out of band so it cannot be lost.
func (o *outbox) close() {
	o.once.Do(func() {
		o.mu.Lock()
		o.closed = true
		close(o.stop)
		o.mu.Unlock()
	})
}

func (o *outbox) idleFor(now time.Time) time.Duration {
	return now.Sub(time.Unix(0, o.lastUsed.Load()))
}

// push enqueues a task. When the queue is full it runs the task on its own
// goroutine instead of blocking the caller.
// outboxInline runs tasks on the caller's goroutine. Enabled in tests so
// assertions can observe sends right after a turn returns.
var outboxInline bool

func (o *outbox) push(fn func()) {
	if fn == nil {
		return
	}
	o.lastUsed.Store(time.Now().UnixNano())
	if outboxInline {
		fn()
		return
	}
	o.mu.RLock()
	if o.closed {
		o.mu.RUnlock()
		// The outbox was retired while this caller still held a reference.
		// Run the task directly so it is not lost.
		go runOutboxTask(fn)
		return
	}
	select {
	case o.ch <- fn:
		o.mu.RUnlock()
	default:
		o.mu.RUnlock()
		slog.Warn("outbox full; running task out of band")
		go runOutboxTask(fn)
	}
}

// outboxFor returns (creating on demand) the sender queue for a session.
func (e *Engine) outboxFor(key string) *outbox {
	if key == "" {
		key = "__global__"
	}
	e.outboxMu.Lock()
	defer e.outboxMu.Unlock()
	if e.outboxes == nil {
		e.outboxes = make(map[string]*outbox)
	}
	if o, ok := e.outboxes[key]; ok {
		return o
	}
	o := newOutbox()
	e.outboxes[key] = o
	e.startOutboxJanitor()
	return o
}

// startOutboxJanitor starts one goroutine per engine that retires sessions'
// outboxes after outboxIdleTimeout without a push. This keeps a long-running
// process (cron new_topic creates a new session each run) from accumulating
// one goroutine per session forever.
func (e *Engine) startOutboxJanitor() {
	e.outboxJanitorOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-e.ctx.Done():
					return
				case now := <-ticker.C:
					e.sweepIdleOutboxes(now)
				}
			}
		}()
	})
}

// sweepIdleOutboxes removes and stops outboxes that have been idle for at
// least outboxIdleTimeout. Split out for tests.
func (e *Engine) sweepIdleOutboxes(now time.Time) {
	e.outboxMu.Lock()
	var retired []*outbox
	for key, o := range e.outboxes {
		if o.idleFor(now) < outboxIdleTimeout {
			continue
		}
		if len(o.ch) > 0 {
			// Never retire an outbox with queued work; give it a fresh idle
			// window so the queue can drain undisturbed.
			o.lastUsed.Store(now.UnixNano())
			continue
		}
		delete(e.outboxes, key)
		retired = append(retired, o)
	}
	e.outboxMu.Unlock()
	for _, o := range retired {
		o.close()
	}
}
