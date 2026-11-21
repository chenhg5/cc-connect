package claudecode

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

// midTurnInterruptRecorder is an in-memory stdin fixture. It never starts a
// CLI or contacts a service; only an interrupt write wakes the test.
type midTurnInterruptRecorder struct {
	interrupts chan struct{}
}

func (r *midTurnInterruptRecorder) Write(p []byte) (int, error) {
	var message struct {
		Type    string `json:"type"`
		Request struct {
			Subtype string `json:"subtype"`
		} `json:"request"`
	}
	if err := json.Unmarshal(p, &message); err != nil {
		return 0, err
	}
	if message.Type == "control_request" && message.Request.Subtype == "interrupt" {
		r.interrupts <- struct{}{}
	}
	return len(p), nil
}

func (*midTurnInterruptRecorder) Close() error { return nil }

// A follow-up starts without another Send: the previous result has already
// cleared turnActive. CancelTurn must still send an interrupt and wait for the
// follow-up's terminating result, rather than taking its idle fast-path.
func TestClaudeSession_FollowUpTurnRemainsCancellable(t *testing.T) {
	cs := newMidTurnTestSession(t, time.Hour)
	cs.transcriptOverride = filepath.Join(t.TempDir(), "missing-transcript.jsonl")
	cs.done = make(chan struct{})
	t.Cleanup(func() { close(cs.done) })
	cs.cancelConfirmTimeout = 2 * time.Second
	stdin := &midTurnInterruptRecorder{interrupts: make(chan struct{}, 1)}
	cs.stdin = stdin

	if err := cs.SendMidTurn("add a follow-up", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	cs.handleReadLoopLine(resultA)
	if cs.turnActive.Load() {
		t.Fatal("the first CLI turn should be inactive after its result")
	}
	cs.handleReadLoopLine(initLine)

	cancelled := make(chan error, 1)
	go func() { cancelled <- cs.CancelTurn() }()
	select {
	case <-stdin.interrupts:
	case err := <-cancelled:
		t.Fatalf("CancelTurn returned %v without interrupting the active follow-up", err)
	case <-time.After(time.Second):
		t.Fatal("CancelTurn did not send an interrupt for the active follow-up")
	}
	select {
	case err := <-cancelled:
		t.Fatalf("CancelTurn returned %v before the follow-up terminated", err)
	default:
	}

	cs.handleReadLoopLine(`{"type":"result","subtype":"error_during_execution","is_error":true,"num_turns":1,"session_id":"test-session"}`)
	select {
	case err := <-cancelled:
		if err != nil {
			t.Fatalf("CancelTurn = %v after the terminating result", err)
		}
	case <-time.After(time.Second):
		t.Fatal("CancelTurn did not observe the terminating result")
	}
	if cs.turnActive.Load() {
		t.Fatal("follow-up still active after its terminating result")
	}
}
