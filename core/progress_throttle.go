package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// CardUpdater performs the actual platform-specific card update.
type CardUpdater interface {
	UpdateCard(ctx context.Context, handle any, messageID string, content string, state ProgressCardState) error
}

// persistedCardSnapshot is the on-disk representation of a card's latest state.
type persistedCardSnapshot struct {
	MessageID string            `json:"message_id"`
	Content   string            `json:"content"`
	State     ProgressCardState `json:"state"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// cardState holds the in-memory state for a single card being throttled.
type cardState struct {
	messageID string
	handle    any
	content   string
	state     ProgressCardState
	updater   CardUpdater
	finalized bool
	pending   bool

	mu sync.RWMutex
}

// cardRegistry persists the latest state of progress cards to disk and
// throttles platform updates via a background ticker.
type cardRegistry struct {
	dir    string
	ticker *time.Ticker
	stopCh chan struct{}

	mu       sync.RWMutex
	cards    map[string]*cardState
	stopOnce sync.Once
}

// NewCardRegistry creates a new in-memory card registry.
// The dir argument is reserved for future disk persistence.
func NewCardRegistry(dir string) *cardRegistry {
	r := &cardRegistry{
		dir:    dir,
		cards:  make(map[string]*cardState),
		stopCh: make(chan struct{}),
		ticker: time.NewTicker(100 * time.Millisecond),
	}
	go r.loop()
	return r
}

func (r *cardRegistry) loop() {
	for {
		select {
		case <-r.ticker.C:
			r.flush()
		case <-r.stopCh:
			return
		}
	}
}

func (r *cardRegistry) flush() {
	r.mu.RLock()
	cards := make([]*cardState, 0, len(r.cards))
	for _, c := range r.cards {
		cards = append(cards, c)
	}
	r.mu.RUnlock()

	for _, c := range cards {
		c.mu.Lock()
		if !c.pending && !c.finalized {
			c.mu.Unlock()
			continue
		}
		handle := c.handle
		content := c.content
		state := c.state
		updater := c.updater
		c.pending = false
		c.mu.Unlock()

		if updater == nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = updater.UpdateCard(ctx, handle, c.messageID, content, state)
		cancel()
	}
}

// UpdateCard stores the latest card content and state. If the card has already
// been finalized and the new state is not final, it returns an error.
func (r *cardRegistry) UpdateCard(messageID string, handle any, content string, state ProgressCardState, updater CardUpdater) error {
	if messageID == "" {
		return errors.New("messageID is required")
	}
	cleanID := sanitizeMessageID(messageID)
	if cleanID == "" {
		return fmt.Errorf("invalid messageID: %s", messageID)
	}
	if updater == nil {
		return errors.New("updater is required")
	}

	r.mu.Lock()
	c, ok := r.cards[cleanID]
	if !ok {
		c = &cardState{messageID: cleanID}
		r.cards[cleanID] = c
	}
	r.mu.Unlock()

	c.mu.Lock()
	if c.finalized && !isFinalProgressCardState(state) {
		c.mu.Unlock()
		return fmt.Errorf("card %s is already finalized", cleanID)
	}
	c.handle = handle
	c.content = content
	c.state = state
	c.updater = updater
	c.pending = true
	c.mu.Unlock()

	r.persistCard(c)
	return nil
}

// Finalize marks a card as finalized. The card must have been previously registered.
func (r *cardRegistry) Finalize(messageID string, state ProgressCardState) error {
	if messageID == "" {
		return errors.New("messageID is required")
	}
	cleanID := sanitizeMessageID(messageID)
	if cleanID == "" {
		return fmt.Errorf("invalid messageID: %s", messageID)
	}

	r.mu.Lock()
	c, ok := r.cards[cleanID]
	r.mu.Unlock()

	if !ok {
		return fmt.Errorf("card %s not registered", cleanID)
	}

	c.mu.Lock()
	c.finalized = true
	c.state = state
	c.pending = true
	c.mu.Unlock()

	r.persistCard(c)
	return nil
}

func isFinalProgressCardState(state ProgressCardState) bool {
	return state == ProgressCardStateCompleted || state == ProgressCardStateFailed
}

func (r *cardRegistry) persistCard(c *cardState) {
	if c == nil {
		return
	}

	c.mu.RLock()
	snap := persistedCardSnapshot{
		MessageID: c.messageID,
		Content:   c.content,
		State:     c.state,
		UpdatedAt: time.Now().UTC(),
	}
	c.mu.RUnlock()

	data, err := json.Marshal(snap)
	if err != nil {
		slog.Error("card registry: marshal card failed", "messageID", c.messageID, "error", err)
		return
	}
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		slog.Error("card registry: mkdir failed", "dir", r.dir, "error", err)
		return
	}
	path := filepath.Join(r.dir, "cc-connect-progress-"+c.messageID+".json")
	if err := AtomicWriteFile(path, data, 0o600); err != nil {
		slog.Error("card registry: atomic write failed", "path", path, "error", err)
	}
}

// LoadPersistedCards reads persisted cards from dir, skips entries whose mtime
// is older than 24 hours, and repopulates the registry. The returned slice is a
// snapshot of the loaded cards.
func (r *cardRegistry) LoadPersistedCards() []*cardState {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.cards = make(map[string]*cardState)

	matches, err := filepath.Glob(filepath.Join(r.dir, "cc-connect-progress-*.json"))
	if err != nil {
		slog.Error("card registry: glob failed", "dir", r.dir, "error", err)
		return nil
	}

	cutoff := time.Now().Add(-24 * time.Hour)
	loaded := make([]*cardState, 0, len(matches))
	for _, path := range matches {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			continue
		}

		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var snap persistedCardSnapshot
		if err := json.Unmarshal(data, &snap); err != nil {
			continue
		}

		c := &cardState{
			messageID: snap.MessageID,
			content:   snap.Content,
			state:     snap.State,
		}
		r.cards[snap.MessageID] = c
		loaded = append(loaded, c)
	}
	return loaded
}

// Stop halts the background ticker.
func (r *cardRegistry) Stop() {
	r.stopOnce.Do(func() {
		r.ticker.Stop()
		close(r.stopCh)
	})
}

// lookup returns a snapshot of the card state for testing.
func (r *cardRegistry) lookup(messageID string) *cardState {
	r.mu.RLock()
	c, ok := r.cards[messageID]
	r.mu.RUnlock()
	if !ok {
		return nil
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	return &cardState{
		messageID: c.messageID,
		handle:    c.handle,
		content:   c.content,
		state:     c.state,
		finalized: c.finalized,
	}
}

// sanitizeMessageID validates and cleans a message ID. It returns an empty
// string for IDs that contain path traversal patterns, path separators, or
// control characters.
func sanitizeMessageID(messageID string) string {
	s := strings.TrimSpace(messageID)
	if s == "" || s == "." || s == ".." {
		return ""
	}
	if strings.ContainsAny(s, "/\\") {
		return ""
	}
	if strings.Contains(s, "..") {
		return ""
	}
	for _, r := range s {
		if r < 32 || r == 127 {
			return ""
		}
	}
	return s
}
