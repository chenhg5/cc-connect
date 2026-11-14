package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// replayCount is deliberately larger than the production events buffer
// (128) so an un-gated replay fills the channel and stalls the read loop.
const replayCount = 400

// replayUpdate builds one session/update notification of the given kind.
func replayUpdate(sessionID, kind string, i int) map[string]any {
	update := map[string]any{"sessionUpdate": kind}
	switch kind {
	case "agent_message_chunk", "user_message_chunk", "agent_thought_chunk":
		update["content"] = map[string]any{"type": "text", "text": fmt.Sprintf("history %d", i)}
	case "tool_call":
		update["toolCallId"] = fmt.Sprintf("tc-%d", i)
		update["title"] = "Bash"
		update["kind"] = "execute"
		update["status"] = "pending"
		update["rawInput"] = map[string]any{"command": "ls"}
	case "tool_call_update":
		update["toolCallId"] = fmt.Sprintf("tc-%d", i)
		update["status"] = "completed"
	case "plan":
		update["entries"] = []any{map[string]any{"content": "step", "status": "pending", "priority": "medium"}}
	}
	return map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/update",
		"params":  map[string]any{"sessionId": sessionID, "update": update},
	}
}

// startReplayServer runs a fake ACP server that answers initialize, then
// answers resumeMethod only after streaming replayCount history
// notifications (plus one current_mode_update mid-replay), the way
// claude-agent-acp replays a long conversation on session/load. After the
// handshake it sends one live agent_message_chunk so the test can check
// that normal delivery resumes.
func startReplayServer(t *testing.T, s *acpSession, resumeMethod string) context.CancelFunc {
	t.Helper()
	clientIn, serverOut := io.Pipe()
	serverIn, clientOut := io.Pipe()
	s.tr = newTransport(clientIn, clientOut, s.onNotification, s.onServerRequest)
	ctx, cancel := context.WithCancel(context.Background())
	go s.tr.readLoop(ctx)

	loadSupported := resumeMethod == "session/load"
	resumeSupported := resumeMethod == "session/resume"
	kinds := []string{"user_message_chunk", "agent_message_chunk", "agent_thought_chunk", "tool_call", "tool_call_update", "plan"}

	go func() {
		defer func() { _ = serverOut.Close() }()
		br := bufio.NewReader(serverIn)
		enc := json.NewEncoder(serverOut)
		for {
			line, err := br.ReadBytes('\n')
			if err != nil {
				return
			}
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if json.Unmarshal(line, &req) != nil || req.Method == "" {
				continue
			}
			switch req.Method {
			case "initialize":
				_ = enc.Encode(rpcResponseMsg{JSONRPC: "2.0", ID: req.ID, Result: initializeResult(loadSupported, resumeSupported, false)})
			case resumeMethod:
				for i := 0; i < replayCount; i++ {
					if i == replayCount/2 {
						_ = enc.Encode(map[string]any{
							"jsonrpc": "2.0",
							"method":  "session/update",
							"params": map[string]any{"sessionId": "sess-old", "update": map[string]any{
								"sessionUpdate": "current_mode_update", "currentModeId": "plan",
							}},
						})
					}
					if err := enc.Encode(replayUpdate("sess-old", kinds[i%len(kinds)], i)); err != nil {
						return
					}
				}
				_ = enc.Encode(rpcResponseMsg{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"sessionId": "sess-old"}})
				_ = enc.Encode(replayUpdate("sess-old", "agent_message_chunk", -1))
			default:
				_ = enc.Encode(rpcResponseMsg{JSONRPC: "2.0", ID: req.ID, Error: &rpcErrPayload{Code: -32601, Message: "method not found"}})
			}
		}
	}()
	return cancel
}

func newReplayTestSession() *acpSession {
	return &acpSession{
		workDir:       "C:\\work",
		events:        make(chan core.Event, 128), // production capacity
		ctx:           context.Background(),
		permByID:      make(map[string]permState),
		toolInputByID: make(map[string]string),
	}
}

// Regression for #1941: a resume that replays more history than the events
// buffer holds must not deadlock the handshake, and the replayed history
// must not leak to the IM as fresh output.
func TestHandshake_LongReplayDoesNotDeadlock(t *testing.T) {
	for _, method := range []string{"session/load", "session/resume"} {
		t.Run(method, func(t *testing.T) {
			s := newReplayTestSession()
			cancel := startReplayServer(t, s, method)
			defer cancel()

			done := make(chan error, 1)
			go func() { done <- s.handshake("sess-old", "") }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("handshake: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("handshake deadlocked: %s replayed %d updates into a %d-slot events channel (#1941)",
					method, replayCount, cap(s.events))
			}

			if got := s.currentACPSessionID(); got != "sess-old" {
				t.Fatalf("session id = %q, want sess-old", got)
			}
			if s.replaying.Load() {
				t.Fatal("replaying flag still set after handshake")
			}
			if got := s.CurrentMode(); got != "plan" {
				t.Fatalf("mode update during replay was dropped: current mode = %q, want plan", got)
			}

			// The only event should be the live chunk sent after the
			// handshake response; no replayed history may precede it.
			select {
			case ev := <-s.events:
				if ev.Type != core.EventText || ev.Content != "history -1" {
					t.Fatalf("first event after handshake = %+v, want the live chunk (replayed history leaked)", ev)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("live update after handshake was not delivered")
			}
		})
	}
}

// A server that never answers must not hang the handshake forever.
func TestHandshake_TimesOutOnUnresponsiveServer(t *testing.T) {
	clientIn, serverOut := io.Pipe()
	serverIn, clientOut := io.Pipe()
	defer func() { _ = serverOut.Close() }()
	go func() { _, _ = io.Copy(io.Discard, serverIn) }()

	s := newReplayTestSession()
	s.handshakeTimeout = 200 * time.Millisecond
	s.tr = newTransport(clientIn, clientOut, s.onNotification, s.onServerRequest)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.tr.readLoop(ctx)

	done := make(chan error, 1)
	go func() { done <- s.handshake("", "") }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("handshake succeeded against a silent server")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handshake ignored its timeout")
	}
}
