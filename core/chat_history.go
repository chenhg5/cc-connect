package core

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	chatHistoryFileName = "chat_history.md"
	chatHistoryHeader   = "<!-- auto-synced by cc-connect · read-only transcript · do not edit -->\n\n"
)

// ChatHistoryWriter appends Telegram Topic messages to a per-workspace
// chat_history.md transcript. It is the file-based replacement for the old
// prompt-injecting on_mention_context mechanism: instead of force-feeding recent
// chatter into every prompt (cross-Topic pollution + token bloat), each Topic's
// conversation is streamed to a plain Markdown file the seat can read on demand.
//
// Isolation is guaranteed by the caller: it only ever passes the workspace
// directory resolved for that specific Topic (resolveWorkspacePattern). A message
// whose Topic does not map to an isolated workspace is dropped before it reaches
// this writer — there is no shared/default file to leak into.
type ChatHistoryWriter struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex // per-file serialization of appends
}

// NewChatHistoryWriter creates a ready-to-use writer.
func NewChatHistoryWriter() *ChatHistoryWriter {
	return &ChatHistoryWriter{locks: make(map[string]*sync.Mutex)}
}

func (w *ChatHistoryWriter) lockFor(path string) *sync.Mutex {
	w.mu.Lock()
	defer w.mu.Unlock()
	m := w.locks[path]
	if m == nil {
		m = &sync.Mutex{}
		w.locks[path] = m
	}
	return m
}

// Append writes one transcript entry to <workspace>/chat_history.md.
//
// It is strictly best-effort and must never disturb the caller's message path:
//   - If workspace, speaker, or body is empty it is a no-op.
//   - If the workspace directory does not exist (not yet created, or being
//     recycled by the idle reaper) the append is skipped silently — the workspace
//     directory is NEVER created here, so idle Topics do not litter the tree.
//   - Any I/O error is logged at debug and swallowed; the real message continues.
//
// Concurrent appends to the same file are serialized so interleaved seats/messages
// in one Topic never corrupt an entry.
func (w *ChatHistoryWriter) Append(workspace, speaker string, ts time.Time, body string) {
	if workspace == "" || speaker == "" {
		return
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return
	}

	// Do not create the workspace dir; only write when it already exists.
	if info, err := os.Stat(workspace); err != nil || !info.IsDir() {
		return
	}

	path := filepath.Join(workspace, chatHistoryFileName)
	m := w.lockFor(path)
	m.Lock()
	defer m.Unlock()

	_, statErr := os.Stat(path)
	newFile := os.IsNotExist(statErr)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		// Dir may have vanished between the stat above and here (reaper race).
		slog.Debug("chat_history: open failed", "path", path, "error", err)
		return
	}
	defer f.Close()

	var sb strings.Builder
	if newFile {
		sb.WriteString(chatHistoryHeader)
	}
	sb.WriteString("## ")
	sb.WriteString(ts.Format("2006-01-02 15:04"))
	sb.WriteString(" — ")
	sb.WriteString(speaker)
	sb.WriteByte('\n')
	sb.WriteString(body)
	sb.WriteString("\n\n")

	if _, err := f.WriteString(sb.String()); err != nil {
		slog.Debug("chat_history: append failed", "path", path, "error", err)
	}
}
