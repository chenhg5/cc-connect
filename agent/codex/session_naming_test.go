package codex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"
)

func TestNativeSessionName_UsesExistingThreadAndPropagatesErrors(t *testing.T) {
	for _, reject := range []bool{false, true} {
		in, out := io.Pipe()
		s := &appServerSession{ctx: context.Background(), stdin: out}
		s.threadID.Store("existing-thread")
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		done := make(chan error, 1)
		go func() { done <- s.SetSessionName(ctx, "容器动态挂载 · Johnson") }()
		var req struct {
			ID     int64
			Method string
			Params map[string]string
		}
		if err := json.NewDecoder(in).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.Method != "thread/name/set" || req.Params["threadId"] != "existing-thread" || req.Params["name"] != "容器动态挂载 · Johnson" {
			t.Fatalf("wrong request: %+v", req)
		}
		resp := rpcResponseEnvelope{ID: req.ID, Result: json.RawMessage(`{}`)}
		if reject {
			resp.Error = &rpcError{Code: -32601, Message: "unsupported"}
		}
		s.handleResponse(resp)
		if err := <-done; (err != nil) != reject {
			t.Fatalf("reject=%v err=%v", reject, err)
		}
		if s.CurrentSessionID() != "existing-thread" {
			t.Fatal("renamed a different thread")
		}
		cancel()
		in.Close()
		out.Close()
	}
}

func TestNativeSessionName_TimeoutDoesNotCloseModelTransport(t *testing.T) {
	stdin := newBlockingWriteCloser()
	defer stdin.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	s := &appServerSession{ctx: context.Background(), stdin: stdin}
	s.threadID.Store("existing-thread")
	s.alive.Store(true)
	if err := s.SetSessionName(ctx, "title"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if stdin.Closed() || !s.Alive() {
		t.Fatal("cosmetic timeout killed model transport")
	}
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	if len(s.pending) != 0 {
		t.Fatal("request leaked")
	}
}
