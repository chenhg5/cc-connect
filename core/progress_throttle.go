package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
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
	messageID         string
	handle            any
	content           string
	state             ProgressCardState
	updater           CardUpdater
	finalized         bool
	pending           bool
	lastPushedContent string
	lastPushTime      time.Time

	mu sync.RWMutex
}

// cardRegistry persists the latest state of progress cards to disk and
// throttles platform updates via a background ticker.
type cardRegistry struct {
	dir string

	mu    sync.RWMutex
	cards map[string]*cardState

	tickerMu sync.Mutex
	ticker   *time.Ticker
	stopCh   chan struct{}
	wg       sync.WaitGroup
	updater  CardUpdater
	interval time.Duration
}

// NewCardRegistry creates a new in-memory card registry and starts a background
// ticker that flushes cards using their per-card updater every 100ms.
// Callers can override the updater and interval via StartTicker.
func NewCardRegistry(dir string) *cardRegistry {
	r := &cardRegistry{
		dir:      dir,
		cards:    make(map[string]*cardState),
		interval: 100 * time.Millisecond,
	}
	r.stopCh = make(chan struct{})
	r.ticker = time.NewTicker(r.interval)
	r.wg.Add(1)
	go r.loop(r.ticker, r.stopCh)
	return r
}

// StartTicker starts a background ticker that scans the registry every interval
// and PATCHes cards whose content has changed and whose last push is at least
// interval ago. A zero or negative interval defaults to 100ms.
func (r *cardRegistry) StartTicker(updater MessageUpdater, interval time.Duration) {
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}

	r.tickerMu.Lock()
	defer r.tickerMu.Unlock()

	if r.ticker != nil {
		r.ticker.Stop()
		close(r.stopCh)
		r.wg.Wait()
	}

	if updater != nil {
		r.updater = &cardUpdaterFromMessageUpdater{updater: updater}
	} else {
		r.updater = nil
	}
	r.interval = interval
	r.stopCh = make(chan struct{})
	ticker := time.NewTicker(interval)
	r.ticker = ticker
	r.wg.Add(1)
	go r.loop(ticker, r.stopCh)
}

// StopTicker stops the background ticker and waits for the current tick to finish.
func (r *cardRegistry) StopTicker() {
	r.tickerMu.Lock()
	defer r.tickerMu.Unlock()

	if r.ticker == nil {
		return
	}
	r.ticker.Stop()
	close(r.stopCh)
	r.wg.Wait()
	r.ticker = nil
	r.stopCh = nil
}

// Stop is an alias for StopTicker for callers that use a single Stop method.
func (r *cardRegistry) Stop() {
	r.StopTicker()
}

func (r *cardRegistry) loop(ticker *time.Ticker, stopCh chan struct{}) {
	defer r.wg.Done()
	for {
		select {
		case <-ticker.C:
			r.tick()
		case <-stopCh:
			return
		}
	}
}

// tick scans all cards and pushes pending updates that are due.
func (r *cardRegistry) tick() {
	r.mu.RLock()
	cards := make([]*cardState, 0, len(r.cards))
	for _, c := range r.cards {
		cards = append(cards, c)
	}
	r.mu.RUnlock()

	now := time.Now()
	for _, c := range cards {
		c.mu.Lock()
		if !c.pending {
			c.mu.Unlock()
			continue
		}
		if c.content == c.lastPushedContent {
			c.pending = false
			c.mu.Unlock()
			continue
		}
		if !c.lastPushTime.IsZero() && now.Sub(c.lastPushTime) < r.interval {
			c.mu.Unlock()
			continue
		}
		handle := c.handle
		content := c.content
		state := c.state
		updater := c.updater
		if updater == nil {
			updater = r.updater
		}
		c.mu.Unlock()

		if updater == nil {
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := updater.UpdateCard(ctx, handle, c.messageID, content, state)
		cancel()
		if err != nil {
			if !isRetriablePatchError(err) {
				c.mu.Lock()
				c.lastPushedContent = content
				c.lastPushTime = now
				c.pending = false
				c.mu.Unlock()
			}
			slog.Warn("card registry: patch failed", "messageID", c.messageID, "state", state, "error", err)
			continue
		}

		c.mu.Lock()
		c.lastPushedContent = content
		c.lastPushTime = now
		c.pending = false
		c.mu.Unlock()
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

// cardUpdaterFromMessageUpdater adapts a MessageUpdater to the CardUpdater
// interface used by the registry's global ticker.
type cardUpdaterFromMessageUpdater struct {
	updater MessageUpdater
}

func (a *cardUpdaterFromMessageUpdater) UpdateCard(ctx context.Context, handle any, _ string, content string, _ ProgressCardState) error {
	return a.updater.UpdateMessage(ctx, handle, content)
}

// RegisterCard records a card whose initial content has already been pushed to
// the platform (e.g. by SendPreviewStart). The card is persisted but not marked
// pending, so the throttler will not re-send the same content. RegisterCard is
// idempotent: repeated registration with the same messageID updates the stored
// handle and content.
func (r *cardRegistry) RegisterCard(messageID string, handle any, content string, updater CardUpdater) error {
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
	c.handle = handle
	c.content = content
	c.updater = updater
	// Content was already pushed by the caller; do not schedule a redundant flush.
	c.pending = false
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
	if c == nil || r.dir == "" {
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
// is older than 24 hours, and repopulates the registry. Loaded cards are
// marked pending so the next ticker tick pushes any content that was persisted
// but not yet flushed to the platform (recovery from mid-window kill).
// The returned slice is a snapshot of the loaded cards.
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
			pending:   true,
		}
		r.cards[snap.MessageID] = c
		loaded = append(loaded, c)
	}
	return loaded
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

// isRetriablePatchError reports whether a failed PATCH should be retried on the
// next tick. Retriable failures include network timeouts, transient connection
// errors, and HTTP 429/5xx responses.
func isRetriablePatchError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ETIMEDOUT) {
		return true
	}

	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "timeout") || strings.Contains(msg, "temporary") {
		return true
	}
	for _, code := range []string{"429", "500", "502", "503", "504"} {
		if strings.Contains(msg, code) {
			return true
		}
	}
	return false
}
