package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// relayTestPlatform records what was sent to it and can be told to fail.
type relayTestPlatform struct {
	mu       sync.Mutex
	sent     []string
	attempts int
	failWith error
	// failFirst: when > 0, only the first N sends fail. When 0 and failWith
	// is set, every send fails.
	failFirst int
}

func (p *relayTestPlatform) Name() string { return "relaytest" }

func (p *relayTestPlatform) Start(MessageHandler) error { return nil }

func (p *relayTestPlatform) Reply(_ context.Context, _ any, c string) error {
	return p.Send(context.Background(), nil, c)
}

func (p *relayTestPlatform) Send(_ context.Context, _ any, c string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failWith != nil && (p.failFirst == 0 || p.attempts < p.failFirst) {
		p.attempts++
		return p.failWith
	}
	p.sent = append(p.sent, c)
	return nil
}

func (p *relayTestPlatform) Stop() error { return nil }

func (p *relayTestPlatform) messages() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]string, len(p.sent))
	copy(cp, p.sent)
	return cp
}

// captureRelayLogs redirects the default logger into a buffer for the
// duration of the test.
func captureRelayLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// relayLogEntries parses the captured JSON log lines.
func relayLogEntries(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("captured log line is not JSON: %q", line)
		}
		out = append(out, m)
	}
	return out
}

// findRelayLog returns the first captured entry with the given message.
func findRelayLog(entries []map[string]any, msg string) map[string]any {
	for _, e := range entries {
		if e["msg"] == msg {
			return e
		}
	}
	return nil
}

// countRelayLog counts the captured entries with the given message.
func countRelayLog(entries []map[string]any, msg string) int {
	n := 0
	for _, e := range entries {
		if e["msg"] == msg {
			n++
		}
	}
	return n
}

// TestRelayUnsolicitedLogsSentWithTriggerSource verifies that a successful
// relay is observable: the message reaches the platform and a
// "unsolicited relay sent" entry records the outcome, the trigger source and
// how long the platform call took. Before #1803 a successful relay was
// silent, so an operator could not tell a delivered message from a dropped
// one.
func TestRelayUnsolicitedLogsSentWithTriggerSource(t *testing.T) {
	buf := captureRelayLogs(t)
	p := &relayTestPlatform{}
	e := NewEngine("test", nil, []Platform{p}, "", LangEnglish)

	e.relayUnsolicited(p, nil, "sess-1", TriggerSourceSubagent, "progress report")

	msgs := p.messages()
	if len(msgs) != 1 || msgs[0] != "progress report" {
		t.Fatalf("expected the report to reach the platform, got %v", msgs)
	}

	entries := relayLogEntries(t, buf)
	entry := findRelayLog(entries, "unsolicited relay sent")
	if entry == nil {
		t.Fatalf("no 'unsolicited relay sent' entry in captured logs:\n%s", buf.String())
	}
	if entry["trigger_source"] != TriggerSourceSubagent {
		t.Fatalf("trigger_source = %v, want %v", entry["trigger_source"], TriggerSourceSubagent)
	}
	if entry["session"] != "sess-1" {
		t.Fatalf("session = %v, want sess-1", entry["session"])
	}
	if _, ok := entry["relay_duration"]; !ok {
		t.Fatalf("relay_duration missing from log entry: %v", entry)
	}
}

// TestRelayUnsolicitedLogsFailure verifies that a failed relay is logged as
// such — with the trigger source — instead of only surfacing as a generic
// platform error.
func TestRelayUnsolicitedLogsFailure(t *testing.T) {
	buf := captureRelayLogs(t)
	p := &relayTestPlatform{failWith: errors.New("platform unavailable")}
	e := NewEngine("test", nil, []Platform{p}, "", LangEnglish)

	e.relayUnsolicited(p, nil, "sess-2", TriggerSourceAgentInitiated, "progress report")

	entries := relayLogEntries(t, buf)
	entry := findRelayLog(entries, "unsolicited relay failed")
	if entry == nil {
		t.Fatalf("no 'unsolicited relay failed' entry in captured logs:\n%s", buf.String())
	}
	if entry["trigger_source"] != TriggerSourceAgentInitiated {
		t.Fatalf("trigger_source = %v, want %v", entry["trigger_source"], TriggerSourceAgentInitiated)
	}
	if countRelayLog(entries, "unsolicited relay sent") != 0 {
		t.Fatal("a failed relay must not be reported as sent")
	}
}

// TestRelayUnsolicitedSplitsLongContent verifies that a reply longer than
// the platform limit is chunked and that every chunk is reported separately.
func TestRelayUnsolicitedSplitsLongContent(t *testing.T) {
	buf := captureRelayLogs(t)
	p := &relayTestPlatform{}
	e := NewEngine("test", nil, []Platform{p}, "", LangEnglish)

	long := strings.Repeat("a", maxPlatformMessageLen+10)
	e.relayUnsolicited(p, nil, "sess-3", TriggerSourceTaskNotification, long)

	msgs := p.messages()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(msgs))
	}
	entries := relayLogEntries(t, buf)
	if got := countRelayLog(entries, "unsolicited relay sent"); got != 2 {
		t.Fatalf("expected 2 'relay sent' entries, got %d", got)
	}
	entry := findRelayLog(entries, "unsolicited relay sent")
	if entry["trigger_source"] != TriggerSourceTaskNotification {
		t.Fatalf("trigger_source = %v, want %v", entry["trigger_source"], TriggerSourceTaskNotification)
	}
}

// TestRelayUnsolicitedContinuesAfterFailure verifies that one failed chunk
// does not prevent the remaining chunks from being delivered.
func TestRelayUnsolicitedContinuesAfterFailure(t *testing.T) {
	buf := captureRelayLogs(t)
	p := &relayTestPlatform{failWith: errors.New("transient failure"), failFirst: 1}
	e := NewEngine("test", nil, []Platform{p}, "", LangEnglish)

	long := strings.Repeat("b", maxPlatformMessageLen+10)
	e.relayUnsolicited(p, nil, "sess-4", TriggerSourceSubagent, long)

	if got := len(p.messages()); got != 1 {
		t.Fatalf("expected 1 delivered chunk, got %d", got)
	}
	entries := relayLogEntries(t, buf)
	if got := countRelayLog(entries, "unsolicited relay failed"); got != 1 {
		t.Fatalf("expected 1 'relay failed' entry, got %d", got)
	}
	if got := countRelayLog(entries, "unsolicited relay sent"); got != 1 {
		t.Fatalf("expected 1 'relay sent' entry, got %d", got)
	}
}

// TestRelayUnsolicitedSkipsEmptyContent verifies that an empty reply is not
// sent and not reported as a relay.
func TestRelayUnsolicitedSkipsEmptyContent(t *testing.T) {
	buf := captureRelayLogs(t)
	p := &relayTestPlatform{}
	e := NewEngine("test", nil, []Platform{p}, "", LangEnglish)

	e.relayUnsolicited(p, nil, "sess-5", TriggerSourceAgentInitiated, "")

	if got := len(p.messages()); got != 0 {
		t.Fatalf("expected no messages, got %d", got)
	}
	if got := countRelayLog(relayLogEntries(t, buf), "unsolicited relay sent"); got != 0 {
		t.Fatalf("expected no 'relay sent' entry, got %d", got)
	}
}

// TestRelayUnsolicitedWithoutPlatform verifies the relay is a no-op (and
// silent) when there is no platform to relay to.
func TestRelayUnsolicitedWithoutPlatform(t *testing.T) {
	buf := captureRelayLogs(t)
	e := NewEngine("test", nil, nil, "", LangEnglish)

	e.relayUnsolicited(nil, nil, "sess-6", TriggerSourceAgentInitiated, "progress report")

	if got := countRelayLog(relayLogEntries(t, buf), "unsolicited relay sent"); got != 0 {
		t.Fatalf("expected no 'relay sent' entry, got %d", got)
	}
}
