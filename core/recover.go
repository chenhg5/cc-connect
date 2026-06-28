package core

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// safeGo launches fn in a new goroutine with panic recovery.
// If fn panics, the panic value and stack trace are logged and written
// to a crash log file under dataDir/crashes/ so operators can diagnose
// unexpected restarts.
func (e *Engine) safeGo(fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				const depth = 32
				pcs := make([]uintptr, depth)
				n := runtime.Callers(2, pcs)
				pcs = pcs[:n]
				frames := runtime.CallersFrames(pcs)

				var stack string
				for {
					frame, more := frames.Next()
					stack += fmt.Sprintf("  %s\n\t%s:%d\n", frame.Function, frame.File, frame.Line)
					if !more {
						break
					}
				}

				slog.Error("panic recovered in goroutine",
					"error", r, "stack", stack, "project", e.name)

				e.writeCrashLog(r, stack)
			}
		}()
		fn()
	}()
}

// writeCrashLog appends a crash record to dataDir/crashes/crash.log.
// Best-effort: failures are logged but never panic.
func (e *Engine) writeCrashLog(r any, stack string) {
	dir := filepath.Join(e.dataDir, "crashes")
	_ = os.MkdirAll(dir, 0o755)

	path := filepath.Join(dir, "crash.log")
	entry := fmt.Sprintf("--- %s ---\npanic: %v\nproject: %s\nstack:\n%s\n\n",
		time.Now().Format(time.RFC3339), r, e.name, stack)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		slog.Error("crash log: open failed", "path", path, "error", err)
		return
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			slog.Error("crash log: close failed", "error", cerr)
		}
	}()
	if _, err := fmt.Fprint(f, entry); err != nil {
		slog.Error("crash log: write failed", "error", err)
	}
}
