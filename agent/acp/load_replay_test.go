package acp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// TestTryLoadSession_LongReplayDoesNotDeadlock is the regression test for the
// production hang where devin-bot stopped replying: session/load replays the
// whole history as session/update notifications before its response. Each
// replayed chunk was emitted into the bounded events channel, which nobody
// reads until StartSession returns; once it filled, the transport read loop
// blocked and the session/load response was never read.
func TestTryLoadSession_LongReplayDoesNotDeadlock(t *testing.T) {
	s, wResp, rReq := newTestSession(t, nil)
	replay := 4 * cap(s.events)
	go func() {
		sc := bufio.NewScanner(rReq)
		for sc.Scan() {
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if json.Unmarshal(sc.Bytes(), &req) != nil || len(req.ID) == 0 {
				continue
			}
			if req.Method == "session/load" {
				for i := 0; i < replay; i++ {
					_, _ = fmt.Fprintf(wResp, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"old-sid","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"old reply %d"}}}}`+"\n", i)
				}
			}
			_, _ = fmt.Fprintf(wResp, `{"jsonrpc":"2.0","id":%s,"result":{}}`+"\n", req.ID)
		}
	}()

	done := make(chan bool, 1)
	go func() { done <- s.tryLoadSession("old-sid") }()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("tryLoadSession returned false, want resumed")
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("tryLoadSession deadlocked replaying %d history chunks (events cap %d)", replay, cap(s.events))
	}
	if n := len(s.events); n != 0 {
		t.Fatalf("replayed history leaked %d events to the engine, want 0", n)
	}
}
