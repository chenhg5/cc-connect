package acp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// TestHandshake_SessionLoadTimeout verifies that when a resume's session/load
// stalls past sessionLoadTimeout, handshake returns core.ErrSessionResumeTimeout
// instead of silently falling through to session/new (which would overwrite the
// saved session ID and truncate history).
func TestHandshake_SessionLoadTimeout(t *testing.T) {
	// Shorten the budget so the test is fast; restore afterwards.
	prev := sessionLoadTimeout
	sessionLoadTimeout = 150 * time.Millisecond
	t.Cleanup(func() { sessionLoadTimeout = prev })

	s, wResp, rReq := newTestSession(t, &fakeCallbacks{})

	// Mock server: answer initialize (loadSession=true), then deliberately
	// never respond to session/load. It must also never receive session/new.
	sawSessionNew := make(chan struct{}, 1)
	go func() {
		sc := bufio.NewScanner(rReq)
		for sc.Scan() {
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
				continue
			}
			switch req.Method {
			case "initialize":
				_, _ = fmt.Fprintf(wResp,
					`{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"agentCapabilities":{"loadSession":true}}}`+"\n",
					req.ID)
			case "session/load":
				// Stall forever — the client's per-call timeout must fire.
			case "session/new":
				select {
				case sawSessionNew <- struct{}{}:
				default:
				}
				_, _ = fmt.Fprintf(wResp,
					`{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"fresh-should-not-happen"}}`+"\n",
					req.ID)
			}
		}
	}()

	err := s.handshake("big-session-id", "")
	if err == nil {
		t.Fatal("handshake returned nil, want ErrSessionResumeTimeout")
	}
	if !errors.Is(err, core.ErrSessionResumeTimeout) {
		t.Fatalf("handshake err = %v, want ErrSessionResumeTimeout", err)
	}

	// Ensure we did NOT fall back to session/new (which would clobber the id).
	select {
	case <-sawSessionNew:
		t.Fatal("handshake started a fresh session/new after load timeout — must preserve saved id")
	case <-time.After(100 * time.Millisecond):
	}
}
