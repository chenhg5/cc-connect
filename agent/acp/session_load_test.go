package acp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// session/load replays saved history as session/update notifications before
// the response. The replay must not fill s.events (nothing reads it during
// handshake) and block the transport read loop.
func TestHandshake_SessionLoadReplayDoesNotBlock(t *testing.T) {
	s, wResp, rReq := newTestSession(t, &fakeCallbacks{})
	s.absorbModes(&acpModesBlock{
		CurrentModeID: "default",
		AvailableModes: []acpModeInfo{
			{ID: "default", Name: "Default"},
			{ID: "plan", Name: "Plan"},
		},
	})

	const replayCount = 200 // above the test cap (32) and production cap (128)
	chunk := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"saved-session-id","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"replayed"}}}}`
	modeUpdate := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"saved-session-id","update":{"sessionUpdate":"current_mode_update","currentModeId":"plan"}}}`

	go func() {
		sc := bufio.NewScanner(rReq)
		for sc.Scan() {
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params struct {
					SessionID string `json:"sessionId"`
				} `json:"params"`
			}
			if json.Unmarshal(sc.Bytes(), &req) != nil {
				continue
			}
			switch req.Method {
			case "initialize":
				_, _ = fmt.Fprintf(wResp, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"agentCapabilities":{"loadSession":true}}}`+"\n", req.ID)
			case "session/load":
				if req.Params.SessionID != "saved-session-id" {
					_, _ = fmt.Fprintf(wResp, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32602,"message":"bad sessionId"}}`+"\n", req.ID)
					return
				}
				for i := 1; i <= replayCount; i++ {
					_, _ = fmt.Fprintln(wResp, chunk)
					if i == 100 {
						_, _ = fmt.Fprintln(wResp, modeUpdate)
					}
				}
				_, _ = fmt.Fprintf(wResp, `{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"saved-session-id"}}`+"\n", req.ID)
				return
			}
		}
	}()

	errCh := make(chan error, 1)
	go func() { errCh <- s.handshake("saved-session-id", "") }()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("handshake: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session/load replay blocked the handshake: s.events filled while nothing was reading it")
	}

	if got := s.currentACPSessionID(); got != "saved-session-id" {
		t.Fatalf("acp session id = %q, want saved-session-id", got)
	}
	if n := len(s.events); n != 0 {
		t.Fatalf("len(s.events) = %d, want 0 (replayed history must not be emitted)", n)
	}
	if got := s.CurrentMode(); got != "plan" {
		t.Fatalf("CurrentMode = %q, want plan (current_mode_update during replay must still apply)", got)
	}

	// After load, updates are emitted again.
	go func() { _, _ = fmt.Fprintln(wResp, chunk) }()
	select {
	case <-s.Events():
	case <-time.After(2 * time.Second):
		t.Fatal("session/update after handshake was not emitted")
	}
}
