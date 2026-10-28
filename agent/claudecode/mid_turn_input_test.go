package claudecode

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

type discardWriteCloser struct{ io.Writer }

func (discardWriteCloser) Close() error { return nil }

func newMidTurnTestSession(t *testing.T, window time.Duration) *claudeSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	prev := followUpTurnWindow
	followUpTurnWindow = window
	t.Cleanup(func() { followUpTurnWindow = prev })
	cs := &claudeSession{
		events: make(chan core.Event, 32),
		ctx:    ctx,
		stdin:  discardWriteCloser{io.Discard},
	}
	cs.sessionID.Store("test-session")
	cs.alive.Store(true)
	return cs
}

// Shapes captured from Claude Code 2.1.280 stream-json output.
const (
	initLine = `{"type":"system","subtype":"init","session_id":"test-session","model":"claude-haiku-4-5"}`
	resultA  = `{"type":"result","subtype":"success","is_error":false,"num_turns":1,"result":"STORY","session_id":"test-session","usage":{"input_tokens":10,"output_tokens":40}}`
	resultB  = `{"type":"result","subtype":"success","is_error":false,"num_turns":1,"result":"BANANA.","session_id":"test-session","usage":{"input_tokens":5,"output_tokens":3}}`
	textB    = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"BANANA."}]},"session_id":"test-session"}`
)

// drain returns the events emitted so far, waiting up to wait for more.
func drain(cs *claudeSession, wait time.Duration) []core.Event {
	var out []core.Event
	deadline := time.After(wait)
	for {
		select {
		case evt := <-cs.events:
			out = append(out, evt)
		case <-deadline:
			return out
		}
	}
}

func results(events []core.Event) []core.Event {
	var out []core.Event
	for _, e := range events {
		if e.Type == core.EventResult {
			out = append(out, e)
		}
	}
	return out
}

func TestClaudeSession_ImplementsMidTurnInputSender(t *testing.T) {
	var s core.AgentSession = &claudeSession{}
	if _, ok := s.(core.MidTurnInputSender); !ok {
		t.Fatal("claudeSession must implement core.MidTurnInputSender")
	}
}

// Without a mid-turn message a result is not held.
func TestClaudeSession_ResultWithoutMidTurnIsImmediate(t *testing.T) {
	cs := newMidTurnTestSession(t, time.Hour)
	cs.handleReadLoopLine(resultA)
	got := results(drain(cs, 50*time.Millisecond))
	if len(got) != 1 || got[0].Content != "STORY" {
		t.Fatalf("results = %#v, want one immediate STORY result", got)
	}
}

// The message was folded into the turn: no follow-up starts, so the held
// result is released unchanged after the window.
func TestClaudeSession_FoldedMidTurnReleasesResultAfterWindow(t *testing.T) {
	cs := newMidTurnTestSession(t, 100*time.Millisecond)
	if err := cs.SendMidTurn("add BANANA", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	cs.handleReadLoopLine(resultA)
	if early := results(drain(cs, 20*time.Millisecond)); len(early) != 0 {
		t.Fatalf("result emitted before the follow-up window: %#v", early)
	}
	got := results(drain(cs, 300*time.Millisecond))
	if len(got) != 1 || got[0].Content != "STORY" || !got[0].Done {
		t.Fatalf("results = %#v, want one Done STORY result", got)
	}
	// Nothing is outstanding any more: the next turn's result is immediate.
	cs.handleReadLoopLine(resultB)
	if next := results(drain(cs, 50*time.Millisecond)); len(next) != 1 || next[0].Content != "BANANA." {
		t.Fatalf("next results = %#v, want immediate BANANA.", next)
	}
}

// The message arrived after the last tool boundary: Claude Code runs it as a
// follow-up turn right after the result. The engine must see one result
// covering both turns, or its reply for the running turn ends early and the
// follow-up's result is taken by the next turn.
func TestClaudeSession_FollowUpTurnMergesIntoRunningTurn(t *testing.T) {
	cs := newMidTurnTestSession(t, time.Hour)
	if err := cs.SendMidTurn("add BANANA", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	cs.handleReadLoopLine(resultA)
	cs.handleReadLoopLine(initLine)
	cs.handleReadLoopLine(textB)
	cs.handleReadLoopLine(resultB)

	got := results(drain(cs, 50*time.Millisecond))
	if len(got) != 1 {
		t.Fatalf("got %d results, want 1 merged result: %#v", len(got), got)
	}
	r := got[0]
	if r.Content != "STORY\n\nBANANA." || !r.Done {
		t.Fatalf("merged content = %q (done=%v), want both turns in order", r.Content, r.Done)
	}
	if r.InputTokens != 15 || r.OutputTokens != 43 {
		t.Fatalf("usage = in %d out %d, want the sum of both turns (15, 43)", r.InputTokens, r.OutputTokens)
	}
}

// Anything else on the stream after a held result means no follow-up turn:
// the result goes out first, in order.
func TestClaudeSession_HeldResultReleasedBeforeNextStreamEvent(t *testing.T) {
	cs := newMidTurnTestSession(t, time.Hour)
	if err := cs.SendMidTurn("add BANANA", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	cs.handleReadLoopLine(resultA)
	cs.handleReadLoopLine(`{"type":"rate_limit_event","rate_limit_info":{}}`)
	if held := results(drain(cs, 20*time.Millisecond)); len(held) != 0 {
		t.Fatalf("rate_limit_event must not release the held result: %#v", held)
	}
	cs.handleReadLoopLine(textB)
	events := drain(cs, 50*time.Millisecond)
	if len(events) < 2 || events[0].Type != core.EventResult || events[0].Content != "STORY" {
		t.Fatalf("events = %#v, want the held STORY result first", events)
	}
	if !strings.Contains(events[1].Content, "BANANA") {
		t.Fatalf("second event = %#v, want the assistant text after the result", events[1])
	}
}

// A process that exits with a held result still delivers it.
func TestClaudeSession_HeldResultFlushedOnExit(t *testing.T) {
	cs := newMidTurnTestSession(t, time.Hour)
	cs.done = make(chan struct{})
	if err := cs.SendMidTurn("add BANANA", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	cs.handleReadLoopLine(resultA)
	waitErr := make(chan error, 1)
	waitErr <- nil
	cs.finishReadLoop(waitErr, nil)
	var got []core.Event
	for evt := range cs.events {
		got = append(got, evt)
	}
	if r := results(got); len(r) != 1 || r[0].Content != "STORY" {
		t.Fatalf("results = %#v, want the held STORY result before the channel closes", r)
	}
}
