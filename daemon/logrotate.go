package daemon

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// RotatingWriter is a thread-safe io.Writer that appends to a log file
// and rotates it when the file exceeds maxSize. Up to maxBackups rotated
// copies are kept (cc-connect.log.1 .. cc-connect.log.N); the oldest is
// discarded on each rotation. The maximum disk usage is therefore
// (1 + maxBackups) × maxSize.
//
// maxBackups must be >= 1; passing a smaller value silently falls back
// to DefaultLogMaxBackups in NewRotatingWriter. A value of 1 reproduces
// the legacy "one backup" behaviour from before #1222.
type RotatingWriter struct {
	mu         sync.Mutex
	file       *os.File
	path       string
	maxSize    int64
	maxBackups int
	curSize    int64
}

// NewRotatingWriter opens (or creates) path for append and returns a
// writer that rotates the file when it grows past maxSize bytes. The
// number of retained backup copies is maxBackups; if maxBackups < 1 the
// caller almost certainly passed an unparsed env var and we fall back
// to DefaultLogMaxBackups so the daemon never silently disables the
// post-mortem trail.
func NewRotatingWriter(path string, maxSize int64, maxBackups int) (*RotatingWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if maxBackups < 1 {
		maxBackups = DefaultLogMaxBackups
	}
	return &RotatingWriter{
		file:       f,
		path:       path,
		maxSize:    maxSize,
		maxBackups: maxBackups,
		curSize:    info.Size(),
	}, nil
}

// MaxBackups returns the configured number of retained backup copies.
// Exposed so callers (and tests) can confirm the resolved value.
func (w *RotatingWriter) MaxBackups() int {
	return w.maxBackups
}

func (w *RotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return 0, os.ErrClosed
	}

	n, err := w.file.Write(p)
	w.curSize += int64(n)

	if w.curSize > w.maxSize {
		w.rotateLocked()
	}
	return n, err
}

// Rotate forces a rotation regardless of the current size. Useful for
// tests and for SIGHUP-style "start a new log file" hooks. Errors are
// logged via slog but never returned, because Write cannot surface
// rotation errors to its caller and the alternative (dropping log
// data) is worse than a missed rotation.
func (w *RotatingWriter) Rotate() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return os.ErrClosed
	}
	w.rotateLocked()
	if w.file == nil {
		return fmt.Errorf("logrotate: reopen after rotation failed for %s", w.path)
	}
	return nil
}

// backupPath returns the rotated-file name for the i-th backup.
// .1 is the most recent, .N is the oldest.
func (w *RotatingWriter) backupPath(i int) string {
	return fmt.Sprintf("%s.%d", w.path, i)
}

// rotateLocked performs the chain rotation: delete the oldest (.N),
// shift .(N-1) -> .N, ... .1 -> .2, rename active -> .1, reopen.
//
// Caller must hold w.mu.
func (w *RotatingWriter) rotateLocked() {
	w.file.Close()

	// 1. Delete the oldest, if it exists. If this fails it is not fatal —
	//    the next rename below will overwrite an empty slot.
	oldest := w.backupPath(w.maxBackups)
	if err := os.Remove(oldest); err != nil && !os.IsNotExist(err) {
		slog.Warn("logrotate: remove oldest failed", "error", err, "path", oldest)
	}

	// 2. Walk the chain from oldest toward newest, shifting each .i -> .(i+1).
	//    We iterate from N-1 down to 1 because each rename frees the source
	//    slot for the previous iteration.
	for i := w.maxBackups - 1; i >= 1; i-- {
		src := w.backupPath(i)
		dst := w.backupPath(i + 1)
		if err := os.Rename(src, dst); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			slog.Warn("logrotate: shift failed", "error", err, "src", src, "dst", dst)
		}
	}

	// 3. Move the active log to .1.
	backup := w.backupPath(1)
	if err := os.Rename(w.path, backup); err != nil {
		slog.Warn("logrotate: rename failed", "error", err, "path", w.path, "backup", backup)
	}

	// 4. Reopen a fresh active log. If this fails w.file becomes nil and
	//    Write() returns os.ErrClosed instead of panicking.
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		w.file = nil
		w.curSize = 0
		return
	}
	w.file = f
	w.curSize = 0
}

func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		return w.file.Close()
	}
	return nil
}
