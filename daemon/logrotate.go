package daemon

import (
	"os"
	"path/filepath"
	"sync"
)

// RotatingWriter is a thread-safe io.Writer that appends to a log file
// and rotates it when the file exceeds maxSize. One backup (.1) is kept,
// so the maximum disk usage is ≈ 2 × maxSize.
type RotatingWriter struct {
	mu      sync.Mutex
	file    *os.File
	path    string
	maxSize int64
	curSize int64
}

func NewRotatingWriter(path string, maxSize int64) (*RotatingWriter, error) {
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
	return &RotatingWriter{
		file:    f,
		path:    path,
		maxSize: maxSize,
		curSize: info.Size(),
	}, nil
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
		w.rotate()
	}
	return n, err
}

func (w *RotatingWriter) rotate() {
	w.file.Close()

	backup := w.path + ".1"
	os.Remove(backup)
	_ = os.Rename(w.path, backup)

	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		// If we cannot open the new log file, w.file will be nil.
		// Write() checks for nil and returns os.ErrClosed instead of panicking.
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
