package core

import (
	"log/slog"
	"sync"
)

// outbox serialises outbound platform calls per session on its own goroutine.
// The turn loop only enqueues, so a slow or hung HTTP call can never keep the
// session lock and block later messages.
type outbox struct {
	ch   chan func()
	once sync.Once
}

const outboxQueueSize = 128

func newOutbox() *outbox {
	o := &outbox{ch: make(chan func(), outboxQueueSize)}
	go func() {
		for fn := range o.ch {
			func() {
				defer func() {
					if r := recover(); r != nil {
						slog.Error("outbox task panicked", "panic", r)
					}
				}()
				fn()
			}()
		}
	}()
	return o
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
	if outboxInline {
		fn()
		return
	}
	select {
	case o.ch <- fn:
	default:
		slog.Warn("outbox full; running task out of band")
		go func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("outbox task panicked", "panic", r)
				}
			}()
			fn()
		}()
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
	return o
}
