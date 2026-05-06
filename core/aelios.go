package core

import (
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"sync/atomic"
	"time"
)

// ── Aelios data types ─────────────────────────────────────────

// AeliosTimelineEntry represents a single timeline card.
type AeliosTimelineEntry struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Content   string `json:"content"`
	Date      string `json:"date,omitempty"`
	Source    string `json:"source,omitempty"`
	CreatedAt string `json:"created_at"`
}

// AeliosSavedEntry represents a single saved / favorite item.
type AeliosSavedEntry struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Content   string `json:"content"`
	Source    string `json:"source,omitempty"`
	CreatedAt string `json:"created_at"`
}

// AeliosDiaryEntry represents a single diary paragraph.
type AeliosDiaryEntry struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Content   string `json:"content"`
	Date      string `json:"date"`
	Time      string `json:"time,omitempty"`
	CreatedAt string `json:"created_at"`
}

// ── Valid type sets ───────────────────────────────────────────

var validTimelineTypes = map[string]bool{
	"chat_summary":    true,
	"agent_task":      true,
	"favorite":        true,
	"diary":           true,
	"memory_update":   true,
	"system_event":    true,
	"file_result":     true,
}

var validSavedTypes = map[string]bool{
	"text": true,
	"link": true,
}

var validDiaryTypes = map[string]bool{
	"manual":        true,
	"daily_summary": true,
	"work":          true,
	"life":          true,
}

// ── ID generators ─────────────────────────────────────────────

func aeliosNewTimelineID() string {
	return "tl_" + generateShortID()
}

func aeliosNewSavedID() string {
	return "fav_" + generateShortID()
}

func aeliosNewDiaryID() string {
	return "diary_" + generateShortID()
}

var idCounter atomic.Uint64

// generateShortID returns a 14-char ID that is unique even under
// high concurrency.  Format: last-10-digits-of-nanosecond + 4-digit
// atomic counter.
// Example: "0615322734" + "0007" → "06153227340007"
func generateShortID() string {
	n := idCounter.Add(1)
	ns := time.Now().UnixNano()
	return fmt.Sprintf("%010d%04d", ns%10_000_000_000, n%10000)
}

// ── Route registration on ManagementServer ────────────────────

// RegisterAeliosRoutes wires /api/v1/aelios/* endpoints into the management mux.
// Call from buildHandler after other route registrations.
func (m *ManagementServer) registerAeliosRoutes(mux *http.ServeMux, prefix string) {
	mux.HandleFunc(prefix+"/aelios/status", m.wrap(m.handleAeliosStatus))

	mux.HandleFunc(prefix+"/aelios/timeline", m.wrap(m.handleAeliosTimeline))
	mux.HandleFunc(prefix+"/aelios/timeline/", m.wrap(m.handleAeliosTimelineByID))

	mux.HandleFunc(prefix+"/aelios/saved", m.wrap(m.handleAeliosSaved))
	mux.HandleFunc(prefix+"/aelios/saved/", m.wrap(m.handleAeliosSavedByID))

	mux.HandleFunc(prefix+"/aelios/diary", m.wrap(m.handleAeliosDiary))
	mux.HandleFunc(prefix+"/aelios/diary/", m.wrap(m.handleAeliosDiaryByID))
}

// ── Store helpers ─────────────────────────────────────────────

// getAeliosStore returns a cached JSONL store for the given collection
// (one of "timeline", "saved", "diary").  The same *AeliosStore is
// returned for the same file path so that concurrent requests share
// one mutex per JSONL file.
func (m *ManagementServer) getAeliosStore(collection string) (*AeliosStore, error) {
	dir, err := AeliosDataDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, collection+".jsonl")

	m.aeliosStoresMu.Lock()
	defer m.aeliosStoresMu.Unlock()

	if s, ok := m.aeliosStores[path]; ok {
		return s, nil
	}
	s, err := NewAeliosStore(path)
	if err != nil {
		return nil, err
	}
	m.aeliosStores[path] = s
	return s, nil
}

// ── Validation helpers ────────────────────────────────────────

var datePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func isValidDate(s string) bool {
	if !datePattern.MatchString(s) {
		return false
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}
