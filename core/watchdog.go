package core

import (
	"bytes"
	"log/slog"
	"runtime/pprof"
	"sync/atomic"
	"time"
)

// maomao fork: diagnostics for turns that hang inside an event handler.
// The event idle timeout only fires while the loop waits in select; if a
// handler blocks, the turn hangs silently (typing forever, session busy).

// stuckHandlerTimeout is how long one event's handling may take before the
// watchdog dumps goroutines and tells the user.
var stuckHandlerTimeout = 10 * time.Minute

// DumpGoroutines logs the stacks of all goroutines (also bound to SIGUSR1).
func DumpGoroutines(reason string) {
	var buf bytes.Buffer
	_ = pprof.Lookup("goroutine").WriteTo(&buf, 2)
	slog.Error("goroutine dump", "reason", reason, "stacks", buf.String())
}

// watchStuckTurn reports once when the handler start time in since stays
// set (non-zero) for longer than stuckHandlerTimeout. Stops when done closes.
func (e *Engine) watchStuckTurn(done <-chan struct{}, since *atomic.Int64, sessionKey string, state *interactiveState) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-done:
			return
		case <-e.ctx.Done():
			return
		case <-tick.C:
		}
		start := since.Load()
		if start == 0 || time.Since(time.Unix(0, start)) < stuckHandlerTimeout {
			continue
		}
		DumpGoroutines("turn stuck in event handler: " + sessionKey)
		// TryLock: the stuck handler may be the one holding state.mu.
		if !state.mu.TryLock() {
			slog.Warn("watchdog: state locked, skipping stuck-turn notice", "session_key", sessionKey)
			return
		}
		p, replyCtx := state.platform, state.replyCtx
		state.mu.Unlock()
		go e.send(p, replyCtx, "⚠️ 这一轮卡住超过 10 分钟，诊断信息已写入日志；发 /new 可重置会话")
		return
	}
}
