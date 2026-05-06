package core

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// AeliosStore provides thread-safe JSONL CRUD for a single file.
// It is used for timeline.jsonl, saved.jsonl, and diary.jsonl.
type AeliosStore struct {
	mu   sync.Mutex
	path string
}

// NewAeliosStore creates a store bound to a JSONL file.
// The parent directory is created if it does not exist.
func NewAeliosStore(path string) (*AeliosStore, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("aelios: mkdir %s: %w", dir, err)
	}
	return &AeliosStore{path: path}, nil
}

// Path returns the underlying file path.
func (s *AeliosStore) Path() string { return s.path }

// AppendJSON marshals v as one JSON line and appends it to the file.
func (s *AeliosStore) AppendJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("aelios: marshal: %w", err)
	}
	data = append(data, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("aelios: open %s: %w", s.path, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("aelios: append %s: %w", s.path, err)
	}
	return f.Close()
}

// ReadAll reads every JSON line from the file and decodes into dst (a *[]T).
// Returns nil error when the file does not exist yet (empty store).
func ReadAllJSONL[T any](s *AeliosStore) ([]T, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return readJSONLFile[T](s.path)
}

// readJSONLFile is the lock-free internal reader (caller must hold s.mu).
func readJSONLFile[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("aelios: open %s: %w", path, err)
	}
	defer f.Close()

	var result []T
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 1 MB max line
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var item T
		if err := json.Unmarshal(line, &item); err != nil {
			continue // skip malformed lines
		}
		result = append(result, item)
	}
	return result, scanner.Err()
}

// DeleteByID reads all entries, filters out the one with matching id,
// and atomically rewrites the file. Returns true if an entry was removed.
func DeleteByIDJSONL[T any](s *AeliosStore, id string, getID func(T) string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	all, err := readJSONLFile[T](s.path)
	if err != nil {
		return false, err
	}

	filtered := make([]T, 0, len(all))
	found := false
	for _, item := range all {
		if getID(item) == id {
			found = true
			continue
		}
		filtered = append(filtered, item)
	}
	if !found {
		return false, nil
	}

	// Rebuild file content.
	var buf []byte
	for _, item := range filtered {
		line, err := json.Marshal(item)
		if err != nil {
			return false, fmt.Errorf("aelios: marshal for rewrite: %w", err)
		}
		buf = append(buf, line...)
		buf = append(buf, '\n')
	}

	if err := AtomicWriteFile(s.path, buf, 0o644); err != nil {
		return false, fmt.Errorf("aelios: atomic write %s: %w", s.path, err)
	}
	return true, nil
}

// ── AeliosDataDir helper ──────────────────────────────────────

// AeliosDataDir returns ~/.cc-connect/aelios/, creating it if needed.
func AeliosDataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("aelios: home dir: %w", err)
	}
	dir := filepath.Join(home, ".cc-connect", "aelios")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("aelios: mkdir %s: %w", dir, err)
	}
	return dir, nil
}
