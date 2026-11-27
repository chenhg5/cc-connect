package claudecode

import (
	"context"
	"encoding/json"
	"io"
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

// Unexpected pre-init events are not proof that no follow-up is coming.
func TestClaudeSession_UnexpectedEventsPreserveExpectedFollowUp(t *testing.T) {
	for _, line := range []string{
		`{"type":"rate_limit_event","rate_limit_info":{}}`,
		`{"type":"system","subtype":"future_event"}`,
		`{"type":"user","message":{"role":"user","content":"echo"}}`,
		`{"type":"future_event"}`,
		textB,
	} {
		for _, withInit := range []bool{false, true} {
			cs := newMidTurnTestSession(t, time.Hour)
			if err := cs.SendMidTurn("continue", "", nil, nil); err != nil {
				t.Fatal(err)
			}
			cs.handleReadLoopLine(resultA)
			cs.handleReadLoopLine(line)
			if got := results(drain(cs, time.Millisecond)); len(got) != 0 {
				t.Fatalf("intermediate event released held result: %#v", got)
			}
			if withInit {
				cs.handleReadLoopLine(initLine)
			}
			cs.handleReadLoopLine(resultB)
			got := results(drain(cs, time.Millisecond))
			if len(got) != 1 || got[0].Content != "STORY\n\nBANANA." {
				t.Fatalf("withInit=%v: got %#v, want one merged result", withInit, got)
			}
		}
	}
}

type midTurnWriteFunc func([]byte) (int, error)

func (f midTurnWriteFunc) Write(p []byte) (int, error) { return f(p) }
func (midTurnWriteFunc) Close() error                  { return nil }

// Send refreshes settings before writing the actual user input. Only a user
// packet can trigger the simulated turn result; get_settings is not a turn.
func midTurnUserWrite(t *testing.T, onUser midTurnWriteFunc) midTurnWriteFunc {
	t.Helper()
	return func(p []byte) (int, error) {
		var packet struct {
			Type    string `json:"type"`
			Request struct {
				Subtype string `json:"subtype"`
			} `json:"request"`
		}
		if err := json.Unmarshal(p, &packet); err != nil {
			t.Fatalf("invalid stdin packet: %v", err)
		}
		if packet.Type == "control_request" && packet.Request.Subtype == "get_settings" {
			return len(p), nil
		}
		if packet.Type != "user" {
			t.Fatalf("unexpected stdin packet type %q", packet.Type)
		}
		return onUser(p)
	}
}

// Settings refresh is best effort. Its failed control write must not roll
// back the independently accepted user input or generate a fake turn result.
func TestClaudeSession_MidTurnSettingsFailureKeepsUserReservation(t *testing.T) {
	cs := newMidTurnTestSession(t, time.Hour)
	settings, users := 0, 0
	cs.stdin = midTurnWriteFunc(func(p []byte) (int, error) {
		var packet struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(p, &packet); err != nil {
			t.Fatal(err)
		}
		switch packet.Type {
		case "control_request":
			settings++
			return 0, io.ErrClosedPipe
		case "user":
			users++
			cs.handleReadLoopLine(resultA)
			return len(p), nil
		default:
			t.Fatalf("unexpected packet type %q", packet.Type)
			return 0, io.ErrUnexpectedEOF
		}
	})
	if err := cs.SendMidTurn("continue", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	if settings != 1 || users != 1 {
		t.Fatalf("writes: settings=%d user=%d, want one of each", settings, users)
	}
	if got := results(drain(cs, time.Millisecond)); len(got) != 0 {
		t.Fatalf("settings failure released pending user turn: %#v", got)
	}
	cs.handleReadLoopLine(resultB)
	if got := results(drain(cs, time.Millisecond)); len(got) != 1 || got[0].Content != "STORY\n\nBANANA." {
		t.Fatalf("accepted input was lost after settings failure: %#v", got)
	}
}

func TestClaudeSession_MidTurnReservesBeforeWriteReturns(t *testing.T) {
	cs := newMidTurnTestSession(t, time.Hour)
	cs.stdin = midTurnUserWrite(t, func(p []byte) (int, error) {
		cs.handleReadLoopLine(resultA)
		return len(p), nil
	})
	if err := cs.SendMidTurn("continue", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := results(drain(cs, time.Millisecond)); len(got) != 0 {
		t.Fatalf("result raced the pending reservation: %#v", got)
	}
	cs.handleReadLoopLine(resultB)
	if got := results(drain(cs, time.Millisecond)); len(got) != 1 || got[0].Content != "STORY\n\nBANANA." {
		t.Fatalf("got %#v, want one merged result", got)
	}
}

func TestClaudeSession_FailedMidTurnWriteRollsBackReservation(t *testing.T) {
	for _, resultDuringWrite := range []bool{false, true} {
		cs := newMidTurnTestSession(t, time.Hour)
		cs.stdin = midTurnUserWrite(t, func([]byte) (int, error) {
			if resultDuringWrite {
				cs.handleReadLoopLine(resultA)
			}
			return 0, io.ErrClosedPipe
		})
		if err := cs.SendMidTurn("continue", "", nil, nil); err == nil {
			t.Fatal("expected write error")
		}
		if !resultDuringWrite {
			cs.handleReadLoopLine(resultA)
		}
		if got := results(drain(cs, time.Millisecond)); len(got) != 1 || got[0].Content != "STORY" {
			t.Fatalf("failed write left result held: %#v", got)
		}
		cs.midTurnMu.Lock()
		pending := len(cs.midTurnPending)
		cs.midTurnMu.Unlock()
		if pending != 0 {
			t.Fatalf("failed write left %d pending messages", pending)
		}
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

func TestClaudeSession_FailedWriteDoesNotRemoveAnotherReservation(t *testing.T) {
	cs := newMidTurnTestSession(t, time.Hour)
	cs.stdin = midTurnUserWrite(t, func([]byte) (int, error) {
		cs.handleReadLoopLine(resultA)
		cs.handleReadLoopLine(initLine)
		// The reader consumed this write's reservation. Model a later
		// independently accepted input before the original writer fails.
		cs.midTurnMu.Lock()
		cs.midTurnNextID++
		cs.midTurnPending = append(cs.midTurnPending, cs.midTurnNextID)
		cs.midTurnMu.Unlock()
		return 0, io.ErrClosedPipe
	})
	if err := cs.SendMidTurn("continue", "", nil, nil); err == nil {
		t.Fatal("expected write error")
	}
	cs.midTurnMu.Lock()
	pending := append([]uint64(nil), cs.midTurnPending...)
	cs.midTurnMu.Unlock()
	if len(pending) != 1 || pending[0] != 2 {
		t.Fatalf("rollback removed another input's reservation: %v", pending)
	}
	cs.handleReadLoopLine(resultB)
	if got := results(drain(cs, time.Millisecond)); len(got) != 0 {
		t.Fatalf("outstanding input must still hold the merged result: %#v", got)
	}
	cs.releaseHeldResult()
	if got := results(drain(cs, time.Millisecond)); len(got) != 1 || got[0].Content != "STORY\n\nBANANA." {
		t.Fatalf("got %#v, want one merged result", got)
	}
}
