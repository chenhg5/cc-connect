package opencode

import (
	"context"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// waitForResult collects events until an EventResult arrives and returns them.
func waitForResult(t *testing.T, ch <-chan core.Event, timeout time.Duration) []core.Event {
	t.Helper()
	var got []core.Event
	deadline := time.After(timeout)
	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				t.Fatalf("event channel closed before a result (%+v)", got)
			}
			got = append(got, evt)
			if evt.Type == core.EventResult {
				return got
			}
		case <-deadline:
			t.Fatalf("no EventResult within %s (got %+v)", timeout, got)
		}
	}
}

// The engine's auto-compress sends the agent's CompressCommand ("/compact"). The
// server transport used to post it as chat text, so the model simply answered it
// and the conversation never shrank — every following turn compressed again. It
// must go to OpenCode's compaction endpoint instead, and still end as one turn.
func TestServerSession_CompactUsesTheSummarizeEndpointNotAChatMessage(t *testing.T) {
	f := newFakeOpencodeServer(t)
	s := newTestServerSession(t, f, "ses_existing")

	if err := s.Send(compactPrompt, "m1", nil, nil); err != nil {
		t.Fatalf("Send(/compact): %v", err)
	}
	waitForResult(t, s.Events(), 5*time.Second)

	calls := f.summarizeCalls()
	if len(calls) != 1 {
		t.Fatalf("summarize calls = %d, want 1", len(calls))
	}
	if calls[0]["providerID"] != "openai" || calls[0]["modelID"] != "gpt-5.6-sol" {
		t.Fatalf("summarize body = %v, want the session's provider/model", calls[0])
	}
	if create, _, msgs := f.counts(); msgs != 0 || create != 0 {
		t.Fatalf("create=%d messages=%d, want 0/0: /compact must not be posted as a chat message", create, msgs)
	}
	if s.turnInFlight.Load() {
		t.Fatal("compaction turn still marked in flight after it finished")
	}
}

// Nothing to compact before the first message: answer at once, touch nothing.
func TestServerSession_CompactWithoutAConversationIsANoop(t *testing.T) {
	f := newFakeOpencodeServer(t)
	s := newTestServerSession(t, f, "")

	if err := s.Send(compactPrompt, "m1", nil, nil); err != nil {
		t.Fatalf("Send(/compact): %v", err)
	}
	waitForResult(t, s.Events(), 3*time.Second)
	if create, _, msgs := f.counts(); create != 0 || msgs != 0 || len(f.summarizeCalls()) != 0 {
		t.Fatalf("create=%d messages=%d summarize=%d, want nothing for an empty session",
			create, msgs, len(f.summarizeCalls()))
	}
}

// A session on OpenCode's default model (none configured in cc-connect) still
// has to name a model to compact: take the one the conversation last answered with.
func TestServerSession_CompactUsesTheConversationModelWhenNoneIsConfigured(t *testing.T) {
	f := newFakeOpencodeServer(t)
	f.resyncMessages = []map[string]any{
		{"info": map[string]any{"id": "msg_u", "role": "user", "sessionID": "ses_existing"}},
		{"info": map[string]any{"id": "msg_a", "role": "assistant", "sessionID": "ses_existing",
			"providerID": "deepseek", "modelID": "deepseek-flash"}},
	}
	cfg := opencodeServeConfig{cmd: "opencode", workDir: "/tmp/ws"}
	s, err := newServerSessionOn(context.Background(), f.srv(), cfg, "", "yolo", "", "ses_existing")
	if err != nil {
		t.Fatalf("newServerSessionOn: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.Send(compactPrompt, "m1", nil, nil); err != nil {
		t.Fatalf("Send(/compact): %v", err)
	}
	waitForResult(t, s.Events(), 5*time.Second)
	calls := f.summarizeCalls()
	if len(calls) != 1 || calls[0]["providerID"] != "deepseek" || calls[0]["modelID"] != "deepseek-flash" {
		t.Fatalf("summarize calls = %v, want the conversation's deepseek/deepseek-flash", calls)
	}
}

// A failed compaction is reported as an error (the engine then keeps draining
// queued messages because the agent is still alive) and does not hang the turn.
func TestServerSession_CompactFailureIsReportedAndDoesNotHang(t *testing.T) {
	f := newFakeOpencodeServer(t)
	f.failSummarize.Store(true)
	s := newTestServerSession(t, f, "ses_existing")

	if err := s.Send(compactPrompt, "m1", nil, nil); err != nil {
		t.Fatalf("Send(/compact): %v", err)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case evt := <-s.Events():
			if evt.Type == core.EventError {
				if s.turnInFlight.Load() {
					t.Fatal("failed compaction left the turn in flight")
				}
				return
			}
			if evt.Type == core.EventResult {
				t.Fatalf("failed compaction reported success: %+v", evt)
			}
		case <-deadline:
			t.Fatal("no error event for a failed compaction")
		}
	}
}

// After auto-compress the engine immediately sends the message that was queued
// meanwhile. That prompt must start a turn of its own and end it with a result —
// the 2026-10-08 incident was a queued turn after a compress that never ended.
func TestServerSession_PromptRightAfterCompactEndsWithItsOwnResult(t *testing.T) {
	f := newFakeOpencodeServer(t)
	s := newTestServerSession(t, f, "ses_existing")

	if err := s.Send(compactPrompt, "m1", nil, nil); err != nil {
		t.Fatalf("Send(/compact): %v", err)
	}
	waitForResult(t, s.Events(), 5*time.Second)

	if err := s.Send("add the leftovers to .gitignore", "m2", nil, nil); err != nil {
		t.Fatalf("queued Send: %v", err)
	}
	// The queued prompt really goes out as a chat message of a new turn.
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, _, msgs := f.counts(); msgs == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("queued prompt never reached the server")
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.emit(map[string]any{"type": "session.idle", "properties": map[string]any{"sessionID": "ses_existing"}})
	waitForResult(t, s.Events(), 5*time.Second)
	if s.turnInFlight.Load() {
		t.Fatal("queued turn still marked in flight after its result")
	}
}
