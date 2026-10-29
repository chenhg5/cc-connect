package codex

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/chenhg5/cc-connect/core"
	"io"
	"strings"
	"testing"
	"time"
)

type reviewFailWriter struct{ n int }

func (w reviewFailWriter) Write(p []byte) (int, error) { return w.n, io.ErrClosedPipe }
func (reviewFailWriter) Close() error                  { return nil }
func TestSteeringReview_ProvenUnwrittenIsRetryable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		writer io.WriteCloser
		retry  bool
	}{
		{"missing stdin", nil, true}, {"zero byte failure", reviewFailWriter{}, true}, {"partial write", reviewFailWriter{n: 1}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s := &appServerSession{ctx: ctx, cancel: cancel, stdin: tc.writer, currentTurn: "turn"}
			s.threadID.Store("thread")
			_, err := s.steerUserInput("answer", 100*time.Millisecond)
			if errors.Is(err, core.ErrSteerRejected) != tc.retry {
				t.Fatalf("retryable=%v, want %v: %v", errors.Is(err, core.ErrSteerRejected), tc.retry, err)
			}
		})
	}
}

func TestSteeringReview_InFlightWriteTimeoutRemainsUnknown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := newBlockingWriteCloser()
	defer w.Close()
	s := &appServerSession{ctx: ctx, cancel: cancel, stdin: w, currentTurn: "turn"}
	s.threadID.Store("thread")
	_, err := s.steerUserInput("answer", 25*time.Millisecond)
	if err == nil || errors.Is(err, core.ErrSteerRejected) {
		t.Fatalf("in-flight write timeout cannot prove no delivery: %v", err)
	}
}

func TestSteeringReview_AnswerOutcomeUsesRealWriter(t *testing.T) {
	for _, tc := range []struct{ name, want string }{{"unwritten", "open"}, {"unacknowledged", "unknown"}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s := &appServerSession{ctx: ctx, cancel: cancel, events: make(chan core.Event, 16)}
			s.threadID.Store("thread")
			s.alive.Store(true)
			acknowledge := transportTestWriter(func(raw []byte) (int, error) {
				var req struct {
					ID     int64  `json:"id"`
					Method string `json:"method"`
				}
				if err := json.Unmarshal(raw, &req); err != nil {
					return 0, err
				}
				result := json.RawMessage(`{"turn":{"id":"active"}}`)
				if req.Method == "turn/steer" {
					result = json.RawMessage(`{"turnId":"active"}`)
				}
				s.handleResponse(rpcResponseEnvelope{ID: req.ID, Result: result})
				return len(raw), nil
			})
			s.stdin = acknowledge
			p := &steeringProcessPlatform{}
			e := core.NewEngine("test", &steeringProcessAgent{s: s}, []core.Platform{p}, "", core.LangEnglish)
			e.SetStreamPreviewCfg(core.StreamPreviewCfg{Enabled: false})
			t.Cleanup(func() { _ = e.Stop() })
			send := func(text string) {
				e.ReceiveMessage(p, &core.Message{SessionKey: "test:writer", Platform: "test", Content: text, ReplyCtx: "ctx"})
			}
			wait := func(check func() bool) {
				t.Helper()
				deadline := time.Now().Add(2 * time.Second)
				for !check() {
					if time.Now().After(deadline) {
						t.Fatal("missing state", p.output())
					}
					time.Sleep(time.Millisecond)
				}
			}
			send("hold task")
			wait(func() bool { s.stateMu.Lock(); defer s.stateMu.Unlock(); return s.currentTurn == "active" })
			s.events <- core.Event{Type: core.EventUserInputRequest, RequestID: "writer-question", Questions: []core.UserQuestion{{Question: "Writer test choice?", Options: []core.UserQuestionOption{{Label: "A"}, {Label: "B"}}}}}
			wait(func() bool { return strings.Contains(p.output(), "Writer test choice?") })
			s.procMu.Lock()
			if tc.name == "unwritten" {
				s.stdin = reviewFailWriter{}
			} else {
				s.stdin = &lockedWriteCloser{}
			}
			s.procMu.Unlock()
			send("/answer B")
			session := e.GetSessions().GetOrCreateActive("test:writer")
			if session.AsyncQuestion.Status != tc.want {
				t.Fatalf("status=%q, want %q", session.AsyncQuestion.Status, tc.want)
			}
			if tc.name == "unwritten" {
				s.procMu.Lock()
				s.stdin = acknowledge
				s.procMu.Unlock()
				send("/answer B")
				if session.AsyncQuestion.Status != "submitted" {
					t.Fatal("proven unwritten answer was not answerable again")
				}
			} else {
				before := len(session.GetHistory(0))
				send("/answer B")
				if len(session.GetHistory(0)) != before {
					t.Fatal("unknown answer replayed")
				}
			}
		})
	}
}
