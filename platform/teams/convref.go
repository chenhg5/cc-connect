package teams

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/chenhg5/cc-connect/core"
)

// storedReplyRef is the persisted subset of a replyContext needed to address a
// proactive (unsolicited) send to a conversation the bot has already seen: the
// Bot Connector serviceURL to POST to, the conversation id, and the bot's own
// account for the outbound `from` envelope. The inbound sender and tenant are
// intentionally omitted — a proactive send has no reply recipient, and the
// connector is single-tenant (config.tenantID already carries it).
type storedReplyRef struct {
	ServiceURL     string         `json:"serviceUrl"`
	ConversationID string         `json:"conversationId"`
	BotAccount     channelAccount `json:"botAccount"`
}

// convRefStore maps an engine session key's conversation component to the reply
// reference captured from that conversation's most recent inbound activity. It
// lets ReconstructReplyCtx rebuild an addressable reply context for a proactive
// send (cron/timer/heartbeat) long after the triggering activity is gone — the
// session key alone encodes the conversation but not the per-activity serviceURL.
// The map is persisted to disk (when a path is configured) so references survive
// a process restart, mirroring the engagement store; an empty path keeps it
// in-memory only (tests / standalone construction).
type convRefStore struct {
	mu   sync.Mutex
	refs map[string]storedReplyRef
	path string
}

// newConvRefStore creates a store, loading any previously persisted references
// from path. An empty path disables persistence.
func newConvRefStore(path string) *convRefStore {
	s := &convRefStore{refs: make(map[string]storedReplyRef), path: path}
	s.load()
	return s
}

// convRefPath locates the per-project conversation-reference store under the
// cc-connect data dir, mirroring the engagement store's convention. Empty inputs
// disable persistence.
func convRefPath(dataDir, project string) string {
	if dataDir == "" || project == "" {
		return ""
	}
	return filepath.Join(dataDir, "teams", sanitizeSegment(project)+"-convrefs.json")
}

func (s *convRefStore) load() {
	if s.path == "" {
		return
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("teams: cannot read conversation-reference store", "path", s.path, "error", err)
		}
		return // missing/unreadable => start empty
	}
	var refs map[string]storedReplyRef
	if err := json.Unmarshal(data, &refs); err != nil {
		slog.Warn("teams: ignoring corrupt conversation-reference store", "path", s.path, "error", err)
		return
	}
	if refs == nil {
		return // e.g. a file whose content is the JSON literal `null`; keep the empty map
	}
	s.refs = refs
}

// upsert records ref under key, persisting only when the stored value actually
// changes. For thread and user scope the stored fields are stable per key, so a
// rewrite happens only on a genuine change (e.g. a serviceURL rotation), keeping
// the webhook path write-rare like engagement.engage. Under channel scope many
// threads share one key while the stored conversationID tracks the active thread,
// so switching active threads rewrites the entry — acceptable, and the write is
// a single bounded AtomicWriteFile. A nil store or empty key is a no-op.
func (s *convRefStore) upsert(key string, ref storedReplyRef) {
	if s == nil || key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.refs[key]; ok && existing == ref {
		return // unchanged — no rewrite
	}
	s.refs[key] = ref
	s.persistLocked()
}

// lookup returns the stored reference for key. A nil store returns not-found.
func (s *convRefStore) lookup(key string) (storedReplyRef, bool) {
	if s == nil {
		return storedReplyRef{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ref, ok := s.refs[key]
	return ref, ok
}

// persistLocked writes the store via core.AtomicWriteFile (temp + fsync +
// rename). The caller must hold s.mu. Mode 0600 (owner-only): the stored
// serviceURL routes the bot's bearer token on a later proactive send, so this
// file is more sensitive than the engagement set. Failures are logged, not fatal
// — a missed persist only costs re-capture on the next inbound after a restart.
func (s *convRefStore) persistLocked() {
	if s.path == "" {
		return
	}
	data, err := json.Marshal(s.refs)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		slog.Warn("teams: cannot create conversation-reference dir", "error", err)
		return
	}
	if err := core.AtomicWriteFile(s.path, data, 0o600); err != nil {
		slog.Warn("teams: cannot persist conversation-reference store", "error", err)
	}
}
