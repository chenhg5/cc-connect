package copilot

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// Respond at the actual RPC boundary, including to an unexpected create, so
// the pre-fix behavior fails immediately instead of waiting for a timeout.
type resumeFailureWriter struct {
	rpc   *rpcClient
	calls []string
}

func (w *resumeFailureWriter) Write(p []byte) (int, error) {
	if strings.HasPrefix(string(p), "Content-Length:") {
		return len(p), nil
	}
	var req jsonRPCRequest
	if err := json.Unmarshal(p, &req); err != nil {
		return 0, err
	}
	w.calls = append(w.calls, req.Method)
	id, _ := json.Marshal(req.ID)
	resp := &jsonRPCResponse{ID: id, Result: json.RawMessage(`{"sessionId":"unexpected-fresh"}`)}
	if req.Method == "session.resume" {
		resp.Error = &jsonRPCError{Code: -1, Message: "session is busy in another window"}
	}
	w.rpc.dispatch(resp)
	return len(p), nil
}

func TestHandshake_ResumeFailureDoesNotCreateNewSession(t *testing.T) {
	w := &resumeFailureWriter{}
	w.rpc = newRPCClient(w)
	cs := &copilotSession{ctx: context.Background(), rpc: w.rpc}
	err := cs.handshake("selected-old")
	if err == nil || !strings.Contains(err.Error(), "session is busy") {
		t.Fatalf("resume failure was not surfaced: %v", err)
	}
	if got := strings.Join(w.calls, ","); got != "ping,session.resume" {
		t.Fatalf("failed resume created another conversation: %s", got)
	}
}
