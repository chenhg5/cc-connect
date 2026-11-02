package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type compressLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *compressLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *compressLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func captureCompressLogs(t *testing.T) *compressLogBuffer {
	t.Helper()
	var buf compressLogBuffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

func compressLogRecords(t *testing.T, buf *compressLogBuffer) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	return records
}

func TestProcessCompressEvents_LogsCompletionOnEveryExit(t *testing.T) {
	for _, auto := range []bool{false, true} {
		for _, outcome := range []string{"result", "error", "eof", "idle_timeout", "stopped", "shutdown"} {
			t.Run(outcome+map[bool]string{false: "/manual", true: "/auto"}[auto], func(t *testing.T) {
				logs := captureCompressLogs(t)
				e := newTestEngine()
				t.Cleanup(e.cancel)
				p := &stubPlatformEngine{n: "test"}
				agentSession := newQueuingSession("compress")
				state := &interactiveState{agentSession: agentSession, platform: p}
				session := e.sessions.GetOrCreateActive("test:user")
				session.SetAgentSessionID("agent-compress", "stub")
				lockGen, _ := session.TryLock()
				defer session.Unlock(lockGen)
				switch outcome {
				case "result":
					agentSession.events <- Event{Type: EventResult, Done: true}
				case "error":
					agentSession.events <- Event{Type: EventError, Error: errors.New("failed")}
				case "eof":
					close(agentSession.events)
				case "idle_timeout":
					e.eventIdleTimeout = time.Millisecond
				case "stopped":
					state.markStopped()
				case "shutdown":
					e.cancel()
				}
				unlocked := false
				e.processCompressEvents(state, session, e.sessions, "test:user", p, nil, &unlocked, auto, lockGen)
				var completions []map[string]any
				for _, record := range compressLogRecords(t, logs) {
					if record["msg"] == "turn complete" && record["compress"] == true {
						completions = append(completions, record)
					}
				}
				if len(completions) != 1 {
					t.Fatalf("got %d compression completion logs, want 1: %s", len(completions), logs)
				}
				r := completions[0]
				if r["outcome"] != outcome || r["auto"] != auto || r["session"] != session.ID || r["agent_session"] != "agent-compress" || r["level"] != "INFO" {
					t.Fatalf("incorrect completion fields: %#v", r)
				}
				if duration, ok := r["turn_duration"].(float64); !ok || duration < 0 {
					t.Fatalf("missing/negative duration: %#v", r)
				}
			})
		}
	}
}

type compressLogQueueSession struct{ *queuingAgentSession }

func (s *compressLogQueueSession) Send(_ string, _ string, _ []ImageAttachment, _ []FileAttachment) error {
	slog.Info("test queued turn sent")
	s.events <- Event{Type: EventResult, Content: "queued reply", Done: true}
	return nil
}

func TestProcessCompressEvents_LogsCompletionBeforeQueuedTurn(t *testing.T) {
	for _, eventType := range []EventType{EventResult, EventError} {
		t.Run(string(eventType), func(t *testing.T) {
			logs := captureCompressLogs(t)
			e := newTestEngine()
			t.Cleanup(e.cancel)
			p := &stubPlatformEngine{n: "test"}
			agentSession := &compressLogQueueSession{newQueuingSession("compress-queue")}
			state := &interactiveState{agentSession: agentSession, platform: p, pendingMessages: []queuedMessage{{content: "next", platform: p}}}
			session := e.sessions.GetOrCreateActive("test:user")
			lockGen, _ := session.TryLock()
			defer session.Unlock(lockGen)
			agentSession.events <- Event{Type: eventType, Done: true, Error: errors.New("failed")}
			unlocked := false
			e.processCompressEvents(state, session, e.sessions, "test:user", p, nil, &unlocked, false, lockGen)
			completionIndex, queueIndex := -1, -1
			for i, r := range compressLogRecords(t, logs) {
				if r["msg"] == "turn complete" && r["compress"] == true {
					if completionIndex != -1 {
						t.Fatal("duplicate compression completion")
					}
					completionIndex = i
				}
				if r["msg"] == "test queued turn sent" {
					queueIndex = i
				}
			}
			if completionIndex < 0 || queueIndex < 0 || completionIndex >= queueIndex {
				t.Fatalf("compression must finish before queued turn starts: %s", logs)
			}
		})
	}
}
