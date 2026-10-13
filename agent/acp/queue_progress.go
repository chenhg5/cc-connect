package acp

import (
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// Trae currently exposes queue positions only on its targeted SSE diagnostic
// channel, not ACP. Do not forward the payload: it also includes queue tokens.
type queueDiagnostics struct {
	mu         sync.Mutex
	partial    string
	dropping   bool
	active     bool
	last       time.Time
	downstream io.Writer
	emit       func(core.Event)
}

var diagnosticANSI = regexp.MustCompile(`\x1b\[[0-9;]*m`)

const queueMarker = "Trae request_wait_in_queue event:"

func (q *queueDiagnostics) begin() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.active = true
	q.last = time.Time{}
	if q.partial != "" {
		q.dropping = true
	}
	q.partial = ""
}
func (q *queueDiagnostics) end() { q.mu.Lock(); defer q.mu.Unlock(); q.active = false }
func (q *queueDiagnostics) Write(b []byte) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, part := range strings.SplitAfter(string(b), "\n") {
		if len(q.partial)+len(part) > stderrTailLimit {
			q.partial = ""
			q.dropping = true
		}
		if !q.dropping {
			q.partial += part
		}
		if !strings.HasSuffix(part, "\n") {
			continue
		}
		if !q.dropping {
			q.line(q.partial)
		}
		q.partial = ""
		q.dropping = false
	}
	return len(b), nil
}
func (q *queueDiagnostics) line(line string) {
	// Suppress all lines bearing the diagnostic marker, even invalid payloads.
	if !strings.Contains(line, queueMarker) {
		if strings.Contains(line, "codex_api::sse::trae_chat:") && strings.Contains(line, "DEBUG") {
			return
		}
		if q.downstream != nil {
			_, _ = q.downstream.Write([]byte(line))
		}
		return
	}
	clean := diagnosticANSI.ReplaceAllString(line, "")
	if !q.active || !strings.Contains(clean, "codex_api::sse::trae_chat:") {
		return
	}
	_, body, _ := strings.Cut(clean, queueMarker)
	var payload struct {
		Position *int `json:"position"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(body)), &payload) != nil || payload.Position == nil || *payload.Position < 0 {
		return
	}
	now := time.Now()
	if !q.last.IsZero() && now.Sub(q.last) < 10*time.Second {
		return
	}
	q.last = now
	if q.emit != nil {
		q.emit(core.Event{Type: core.EventProgress, Progress: &core.RuntimeProgress{Phase: "waiting_model", QueuePosition: *payload.Position}})
	}
}
func queueDiagnosticEnv(env []string) []string {
	var filter string
	for _, s := range env {
		if strings.HasPrefix(s, "RUST_LOG=") {
			filter = strings.TrimPrefix(s, "RUST_LOG=")
		}
	}
	var parts []string
	for _, s := range strings.Split(filter, ",") {
		if s != "" && !strings.HasPrefix(s, "codex_api::sse::trae_chat=") {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		parts = append(parts, "warn")
	}
	parts = append(parts, "codex_api::sse::trae_chat=debug")
	return core.MergeEnv(env, []string{"RUST_LOG=" + strings.Join(parts, ",")})
}
