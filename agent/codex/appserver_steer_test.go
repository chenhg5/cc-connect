package codex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

type steerTestWriter struct{ write func([]byte) (int, error) }

func (w steerTestWriter) Write(p []byte) (int, error) { return w.write(p) }
func (w steerTestWriter) Close() error                { return nil }

func steerTestSession(t *testing.T, respond func(*appServerSession, map[string]any)) *appServerSession {
	t.Helper()
	s := &appServerSession{ctx: context.Background(), workDir: t.TempDir(), currentTurn: "turn-1", pendingMsgs: []string{"existing output"}, events: make(chan core.Event, 10)}
	s.alive.Store(true)
	s.threadID.Store("thread-1")
	s.stdin = steerTestWriter{write: func(p []byte) (int, error) {
		var request map[string]any
		if err := json.Unmarshal(p, &request); err != nil {
			return 0, err
		}
		respond(s, request)
		return len(p), nil
	}}
	return s
}

func TestAppServerSession_SteerPreservesActiveTurnAndAttachments(t *testing.T) {
	requests := make(chan map[string]any, 1)
	s := steerTestSession(t, func(s *appServerSession, req map[string]any) {
		requests <- req
		s.handleResponse(rpcResponseEnvelope{ID: req["id"], Result: json.RawMessage(`{"turnId":"turn-1"}`)})
	})
	err := s.Steer(context.Background(), "turn-1", "inspect only", []core.ImageAttachment{{MimeType: "image/png", Data: []byte("png-data")}}, []core.FileAttachment{{FileName: "report.txt", Data: []byte("file-data")}})
	if err != nil {
		t.Fatal(err)
	}
	req := <-requests
	if req["method"] != "turn/steer" {
		t.Fatalf("method = %v", req["method"])
	}
	params := req["params"].(map[string]any)
	if params["expectedTurnId"] != "turn-1" || params["threadId"] != "thread-1" {
		t.Fatalf("params = %#v", params)
	}
	input := params["input"].([]any)
	text := input[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "inspect only") || !strings.Contains(text, "report.txt") {
		t.Fatalf("text = %q", text)
	}
	files, err := os.ReadDir(s.workDir + "/.cc-connect/attachments")
	if err != nil || len(files) != 1 {
		t.Fatalf("staged files: %v %v", files, err)
	}
	image := input[1].(map[string]any)
	data, err := os.ReadFile(image["path"].(string))
	if err != nil || string(data) != "png-data" || image["type"] != "localImage" {
		t.Fatalf("image = %#v, data %q, err %v", image, data, err)
	}
	if s.CurrentTurnID() != "turn-1" || len(s.pendingMsgs) != 1 {
		t.Fatal("steer replaced the active turn or discarded pending output")
	}
}

func TestAppServerSession_SteerRejectsMissingOrMismatchedTurnWithoutWriting(t *testing.T) {
	for _, expected := range []string{"", "turn-2"} {
		t.Run(expected, func(t *testing.T) {
			s := steerTestSession(t, func(_ *appServerSession, _ map[string]any) { t.Error("unexpected request") })
			if err := s.Steer(context.Background(), expected, "input", nil, nil); !errors.Is(err, core.ErrSteerNotActive) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	s := steerTestSession(t, func(_ *appServerSession, _ map[string]any) { t.Error("unexpected request") })
	s.finishTurn("turn-1", nil)
	if err := s.Steer(context.Background(), "turn-1", "input", nil, nil); !errors.Is(err, core.ErrSteerNotActive) {
		t.Fatalf("ended turn error = %v", err)
	}
}

func TestAppServerSession_SteerDistinguishesRejectionFromUnknownOutcome(t *testing.T) {
	for _, test := range []struct {
		name     string
		response rpcResponseEnvelope
		unknown  bool
	}{
		{"rpc rejection", rpcResponseEnvelope{Error: &rpcError{Code: -32600, Message: "expected turn mismatch"}}, false},
		{"disconnect", rpcResponseEnvelope{TransportError: io.EOF}, true},
		{"malformed response", rpcResponseEnvelope{Result: json.RawMessage(`{`)}, true},
		{"wrong accepted turn", rpcResponseEnvelope{Result: json.RawMessage(`{"turnId":"turn-2"}`)}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := steerTestSession(t, func(s *appServerSession, req map[string]any) {
				resp := test.response
				resp.ID = req["id"]
				s.handleResponse(resp)
			})
			err := s.Steer(context.Background(), "turn-1", "input", nil, nil)
			if err == nil || errors.Is(err, core.ErrSteerOutcomeUnknown) != test.unknown {
				t.Fatalf("error = %v, unknown want %v", err, test.unknown)
			}
		})
	}
}

func TestAppServerSession_SteerTimeoutAndWriteFailureHaveUnknownOutcome(t *testing.T) {
	for _, writeFailure := range []bool{false, true} {
		s := steerTestSession(t, func(_ *appServerSession, _ map[string]any) {})
		if writeFailure {
			s.stdin = steerTestWriter{write: func(p []byte) (int, error) { return 1, io.ErrShortWrite }}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
		err := s.Steer(ctx, "turn-1", "input", nil, nil)
		cancel()
		if !errors.Is(err, core.ErrSteerOutcomeUnknown) {
			t.Fatalf("writeFailure %v error = %v", writeFailure, err)
		}
		s.pendingMu.Lock()
		pending := len(s.pending)
		s.pendingMu.Unlock()
		if pending != 0 {
			t.Fatalf("leaked %d pending requests", pending)
		}
	}
}

func TestAppServerSession_SteerAcceptedBeforeTurnEndsStillSucceeds(t *testing.T) {
	s := steerTestSession(t, func(s *appServerSession, req map[string]any) {
		s.finishTurn("turn-1", nil)
		s.handleResponse(rpcResponseEnvelope{ID: req["id"], Result: json.RawMessage(`{"turnId":"turn-1"}`)})
	})
	if err := s.Steer(context.Background(), "turn-1", "input", nil, nil); err != nil {
		t.Fatal(err)
	}
	if s.CurrentTurnID() != "" {
		t.Fatal("completed turn resurrected")
	}
}

func TestAppServerSession_SendDoesNotResurrectCompletedTurn(t *testing.T) {
	s := steerTestSession(t, func(s *appServerSession, req map[string]any) {
		s.handleNotification("turn/started", json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-2"}}`))
		s.handleNotification("turn/completed", json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-2","status":"completed"}}`))
		s.handleResponse(rpcResponseEnvelope{ID: req["id"], Result: json.RawMessage(`{"turn":{"id":"turn-2"}}`)})
	})
	s.currentTurn = ""
	if err := s.Send("input", "msg-1", nil, nil); err != nil {
		t.Fatal(err)
	}
	if s.CurrentTurnID() != "" {
		t.Fatal("turn/start response resurrected completed turn")
	}
}

func TestAppServerSession_StaleTurnNotificationsCannotReplaceOrFinishActiveTurn(t *testing.T) {
	s := steerTestSession(t, func(_ *appServerSession, _ map[string]any) {})
	for _, notif := range []struct{ method, params string }{
		{"turn/completed", `{"threadId":"other-thread","turn":{"id":"turn-1"}}`},
		{"turn/completed", `{"threadId":"thread-1","turn":{"id":"old-turn"}}`},
		{"turn/started", `{"threadId":"thread-1","turn":{"id":"old-turn"}}`},
		{"turn/started", `{"threadId":"thread-1","turn":{"id":"turn-1"}}`},
	} {
		s.handleNotification(notif.method, json.RawMessage(notif.params))
		if s.CurrentTurnID() != "turn-1" || len(s.pendingMsgs) != 1 {
			t.Fatalf("notification %s %s changed active turn", notif.method, notif.params)
		}
	}
}

func TestAppServerSession_StaleIdleCannotCompleteNewTurn(t *testing.T) {
	s := steerTestSession(t, func(_ *appServerSession, _ map[string]any) {})
	s.handleNotification("turn/completed", json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed"}}`))
	// A queued turn can start as soon as the completed event reaches the engine.
	for len(s.events) > 0 {
		<-s.events
	}
	s.handleNotification("turn/started", json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-2"}}`))
	s.handleNotification("thread/status/changed", json.RawMessage(`{"threadId":"thread-1","status":{"type":"idle"}}`))
	if s.CurrentTurnID() != "turn-2" {
		t.Fatal("unscoped idle completed a newer turn")
	}
	select {
	case event := <-s.events:
		t.Fatalf("stale idle emitted event %#v", event)
	default:
	}
	s.handleNotification("turn/completed", json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-2","status":"completed"}}`))
	if s.CurrentTurnID() != "" {
		t.Fatal("matching completion did not finish the turn")
	}
	if event := <-s.events; event.Type != core.EventResult || !event.Done {
		t.Fatalf("matching completion = %#v", event)
	}
}
