package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"log/slog"
)

// PendingReplyEntry is one queued-but-unsent reply persisted to disk so it
// can be replayed after a hard kill (OOM, panic, SIGKILL) that bypasses the
// graceful drain path. See issue #1804.
//
// Only the most recent reply is kept. The user perceives a "lost reply" as a
// silent failure of the most recent agent turn, so older entries are useless
// once a newer one is queued.
type PendingReplyEntry struct {
	Platform    string    `json:"platform"`
	SessionKey  string    `json:"session_key"`
	Content     string    `json:"content"`
	ContentHash string    `json:"content_hash"` // SHA-256 of Content, hex-encoded
	CreatedAt   time.Time `json:"created_at"`
}

// replyJournalDir is the directory inside dataDir that holds the journal
// file. Reuses the existing `run/` namespace that already holds
// restart_notify and the API socket.
const replyJournalDir = "run"

// replyJournalFile is the journal file name. Mode 0o600 because the content
// can include user-visible chat history that may carry sensitive data.
const replyJournalFile = "last_reply.json"

// replyJournalTimeout caps the replay-on-startup attempt. Short by design:
// if the platform API is unreachable we want to give up quickly so the daemon
// can finish starting.
const replyJournalTimeout = 10 * time.Second

// ReplyJournalPath returns the absolute path of the journal file inside
// dataDir. Exported so tests and the management API can introspect it.
func ReplyJournalPath(dataDir string) string {
	return filepath.Join(dataDir, replyJournalDir, replyJournalFile)
}

// HashContent computes a stable SHA-256 hex digest of a reply's content.
// Used by the dedup layer so the replay path can silently ignore
// "duplicate message" errors returned by platform APIs.
func HashContent(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// WritePendingReply persists a pending reply entry, replacing any previous
// entry. File mode 0o600: the content may contain sensitive chat history.
//
// An empty dataDir is a programming error (the engine should never have a
// journal without a data dir) and is reported as such so misconfigured
// deployments surface loudly instead of silently dropping replies.
func WritePendingReply(dataDir string, entry PendingReplyEntry) error {
	if dataDir == "" {
		return errors.New("reply journal: empty dataDir")
	}
	dir := filepath.Join(dataDir, replyJournalDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// Hash the content if the caller didn't pre-compute it. Doing it here
	// keeps the call sites terse.
	if entry.ContentHash == "" {
		entry.ContentHash = HashContent(entry.Content)
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return AtomicWriteFile(ReplyJournalPath(dataDir), data, 0o600)
}

// ClearPendingReply removes the journal entry. Idempotent: a missing file
// is not an error. Called after a reply succeeds (or after a successful
// replay on startup).
func ClearPendingReply(dataDir string) error {
	p := ReplyJournalPath(dataDir)
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ConsumePendingReply reads and atomically deletes the journal entry.
// Returns nil if no entry exists or the file is malformed (we don't want
// a corrupt journal to block startup indefinitely).
func ConsumePendingReply(dataDir string) *PendingReplyEntry {
	if dataDir == "" {
		return nil
	}
	p := ReplyJournalPath(dataDir)
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	// Best-effort delete. We delete before parsing so a malformed entry
	// doesn't loop forever across restarts.
	_ = os.Remove(p)
	var e PendingReplyEntry
	if err := json.Unmarshal(data, &e); err != nil {
		slog.Warn("reply journal: malformed entry discarded", "path", p, "error", err)
		return nil
	}
	return &e
}

// ReplayResult describes the outcome of a single replay attempt. Exposed for
// tests and for the management API to surface journal state to operators.
type ReplayResult struct {
	Delivered    bool
	Duplicate    bool   // platform returned "duplicate" error
	ErrorMessage string // platform error, if any
}

// ReplayPendingReply attempts to re-send a pending reply via the platform
// that originally queued it. Uses a fresh context with replayJournalTimeout
// so the replay is not aborted by an in-flight shutdown.
//
// On success, the journal is cleared. On "duplicate" errors (the platform
// API says the message was already sent), the journal is also cleared —
// the user's intent is satisfied. On other errors, the journal is left in
// place for the next restart to retry.
//
// Platforms without a ReplyContextReconstructor cannot reconstruct a reply
// target from a session key alone (e.g. Feishu needs the message_id); in
// that case the journal is left intact and a warning is logged.
func ReplayPendingReply(dataDir string, platforms []Platform) ReplayResult {
	entry := ConsumePendingReply(dataDir)
	if entry == nil {
		return ReplayResult{}
	}

	var target Platform
	for _, p := range platforms {
		if p.Name() == entry.Platform {
			target = p
			break
		}
	}
	if target == nil {
		slog.Warn("reply journal: platform not found, leaving entry for inspection",
			"platform", entry.Platform, "session", entry.SessionKey, "path", ReplyJournalPath(dataDir))
		// Put the entry back so an operator with the right platform installed
		// can retry. Use a "move aside" rename rather than a fresh write so
		// we preserve the original timestamp.
		if data, err := json.Marshal(entry); err == nil {
			_ = AtomicWriteFile(ReplyJournalPath(dataDir), data, 0o600)
		}
		return ReplayResult{ErrorMessage: "platform not loaded: " + entry.Platform}
	}

	rcr, ok := target.(ReplyContextReconstructor)
	if !ok {
		slog.Warn("reply journal: platform cannot reconstruct reply ctx from session key; skipping replay",
			"platform", entry.Platform, "session", entry.SessionKey)
		// Re-persist so a future build with a richer platform adapter can
		// retry. Without this the entry would be silently lost because
		// ConsumePendingReply already removed the file from disk.
		if data, err := json.Marshal(entry); err == nil {
			_ = AtomicWriteFile(ReplyJournalPath(dataDir), data, 0o600)
		}
		return ReplayResult{ErrorMessage: "platform does not implement ReplyContextReconstructor"}
	}

	replyCtx, err := rcr.ReconstructReplyCtx(entry.SessionKey)
	if err != nil {
		slog.Warn("reply journal: ReconstructReplyCtx failed; leaving entry for retry",
			"platform", entry.Platform, "session", entry.SessionKey, "error", err)
		// Re-persist so the next restart tries again.
		if data, mErr := json.Marshal(entry); mErr == nil {
			_ = AtomicWriteFile(ReplyJournalPath(dataDir), data, 0o600)
		}
		return ReplayResult{ErrorMessage: err.Error()}
	}

	ctx, cancel := contextWithTimeoutForJournal()
	defer cancel()

	if err := target.Reply(ctx, replyCtx, entry.Content); err != nil {
		if isDuplicateReplyError(err) {
			slog.Info("reply journal: platform reported duplicate; treating as delivered",
				"platform", entry.Platform, "session", entry.SessionKey, "content_hash", entry.ContentHash)
			return ReplayResult{Delivered: true, Duplicate: true}
		}
		slog.Warn("reply journal: replay failed; leaving entry for next restart",
			"platform", entry.Platform, "session", entry.SessionKey, "error", err)
		// Re-persist for the next restart.
		if data, mErr := json.Marshal(entry); mErr == nil {
			_ = AtomicWriteFile(ReplyJournalPath(dataDir), data, 0o600)
		}
		return ReplayResult{ErrorMessage: err.Error()}
	}

	slog.Info("reply journal: replay succeeded",
		"platform", entry.Platform, "session", entry.SessionKey,
		"content_hash", entry.ContentHash, "content_len", len(entry.Content))
	return ReplayResult{Delivered: true}
}

// contextWithTimeoutForJournal returns a fresh context.Background() with a
// replyJournalTimeout deadline. Replay must not be cancelled by an
// in-flight shutdown, so we do NOT derive from e.ctx.
func contextWithTimeoutForJournal() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), replyJournalTimeout)
}

// isDuplicateReplyError returns true if the platform API response indicates
// the message was already delivered (the canonical case is Feishu's
// "message already exists" / "duplicate" error). Strings-based check
// because each platform returns a slightly different message; centralising
// the heuristic here keeps platform adapters from having to opt in.
//
// Conservative by design: false negatives cause a duplicate send to the
// user, false positives silently drop a reply. We'd rather send twice than
// drop.
func isDuplicateReplyError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"duplicate",
		"already exists",
		"already sent",
		"already delivered",
		"already in chat",
		"message is duplicate",
		"重复", // zh
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}
