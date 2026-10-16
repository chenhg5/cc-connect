package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// historyProbeTimeout bounds a one-shot session/load used only to read
// history. Same budget as session/list: fail rather than block /history.
var historyProbeTimeout = 15 * time.Second

// historyLoadGrace waits for history chunks that arrive just after the
// session/load response. Devin replays before the response; the grace
// covers agents that flush a trailing chunk afterwards.
var historyLoadGrace = 300 * time.Millisecond

type historyChunk struct {
	role  string
	text  string
	ts    time.Time
	hasTS bool
}

type historyCollector struct {
	mu     sync.Mutex
	wantID string
	chunks []historyChunk
}

func (h *historyCollector) onNotification(method string, params json.RawMessage) {
	if method != "session/update" {
		return
	}
	ch, ok := historyChunkFromUpdate(h.wantID, params)
	if !ok {
		return
	}
	h.mu.Lock()
	h.chunks = append(h.chunks, ch)
	h.mu.Unlock()
}

func (h *historyCollector) entries() []core.HistoryEntry {
	h.mu.Lock()
	defer h.mu.Unlock()
	return mergeHistoryChunks(h.chunks)
}

// GetSessionHistory loads one session in a short-lived probe and returns
// user/assistant text. Thoughts and tool calls are ignored. limit <= 0
// returns the full merged transcript; otherwise the last limit entries.
func (a *Agent) GetSessionHistory(ctx context.Context, sessionID string, limit int) ([]core.HistoryEntry, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("acp: session id is required")
	}
	cwd := a.canonicalWorkDir()
	probeCtx, cancel := context.WithTimeout(ctx, historyProbeTimeout)
	defer cancel()

	col := &historyCollector{wantID: sessionID}
	tr, stderrBuf, teardown, err := a.probeSpawn(probeCtx, cwd, col.onNotification)
	if err != nil {
		return nil, err
	}
	defer teardown()

	init, err := probeInitialize(probeCtx, tr)
	if err != nil {
		return nil, wrapProbeStderr(err, stderrBuf)
	}
	a.noteCapabilities(init)
	if !sessionLoadSupported(init) {
		return nil, fmt.Errorf("acp: session/load is not supported by this agent")
	}
	entries, err := loadHistory(probeCtx, tr, col, sessionID, cwd, historyLoadGrace)
	if err != nil {
		return nil, err
	}
	return limitHistory(entries, limit), nil
}

func loadHistory(ctx context.Context, tr *transport, col *historyCollector, sessionID, cwd string, grace time.Duration) ([]core.HistoryEntry, error) {
	_, err := tr.call(ctx, "session/load", map[string]any{
		"sessionId":  sessionID,
		"cwd":        cwd,
		"mcpServers": []any{},
	})
	if err != nil {
		return nil, fmt.Errorf("acp: session/load: %w", err)
	}
	if grace > 0 {
		timer := time.NewTimer(grace)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	return col.entries(), nil
}

func historyChunkFromUpdate(wantID string, params json.RawMessage) (historyChunk, bool) {
	kind, update, sid := peekSessionUpdate(params)
	if sid != "" && wantID != "" && sid != wantID {
		return historyChunk{}, false
	}
	var role string
	switch kind {
	case "user_message_chunk":
		role = "user"
	case "agent_message_chunk":
		role = "assistant"
	default:
		return historyChunk{}, false
	}
	text := acpMessageText(update)
	if text == "" {
		return historyChunk{}, false
	}
	ch := historyChunk{role: role, text: text}
	if ts, ok := parseMetaTimestamp(update); ok {
		ch.ts = ts
		ch.hasTS = true
	}
	return ch, true
}

func mergeHistoryChunks(chunks []historyChunk) []core.HistoryEntry {
	var out []core.HistoryEntry
	for _, c := range chunks {
		if n := len(out); n > 0 && out[n-1].Role == c.role {
			out[n-1].Content += c.text
			if out[n-1].Timestamp.IsZero() && c.hasTS {
				out[n-1].Timestamp = c.ts
			}
			continue
		}
		e := core.HistoryEntry{Role: c.role, Content: c.text}
		if c.hasTS {
			e.Timestamp = c.ts
		}
		out = append(out, e)
	}
	return out
}

func limitHistory(entries []core.HistoryEntry, limit int) []core.HistoryEntry {
	if limit > 0 && len(entries) > limit {
		return append([]core.HistoryEntry(nil), entries[len(entries)-limit:]...)
	}
	return entries
}

func parseMetaTimestamp(update json.RawMessage) (time.Time, bool) {
	var wrap struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if json.Unmarshal(update, &wrap) != nil || wrap.Meta == nil {
		return time.Time{}, false
	}
	raw, ok := wrap.Meta["cognition.ai/timestamp"]
	if !ok {
		return time.Time{}, false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil || strings.TrimSpace(s) == "" {
		return time.Time{}, false
	}
	ts, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		if ts, err = time.Parse(time.RFC3339, s); err != nil {
			return time.Time{}, false
		}
	}
	return ts.Local(), true
}

// peekSessionUpdate splits a session/update notification into kind, inner
// update object, and session id. ok is implied by a non-empty kind.
func peekSessionUpdate(params json.RawMessage) (kind string, update json.RawMessage, sessionID string) {
	if len(bytes.TrimSpace(params)) == 0 {
		return "", nil, ""
	}
	var wrap struct {
		SessionID string          `json:"sessionId"`
		Update    json.RawMessage `json:"update"`
	}
	if json.Unmarshal(params, &wrap) != nil || len(wrap.Update) == 0 {
		return "", nil, ""
	}
	var head struct {
		Kind string `json:"sessionUpdate"`
	}
	if json.Unmarshal(wrap.Update, &head) != nil {
		return "", wrap.Update, wrap.SessionID
	}
	return head.Kind, wrap.Update, wrap.SessionID
}
