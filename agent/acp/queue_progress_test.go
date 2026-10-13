package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/chenhg5/cc-connect/core"
	"os"
	"strings"
	"testing"
	"time"
)

func TestQueueDiagnostics_FragmentedPrivatePayloadTurnScopeAndThrottle(t *testing.T) {
	var output bytes.Buffer
	var events []core.Event
	q := &queueDiagnostics{downstream: &output, emit: func(e core.Event) { events = append(events, e) }}
	line := "\x1b[34mDEBUG codex_api::sse::trae_chat:\x1b[0m Trae request_wait_in_queue event: {\"position\":482,\"queue_id\":\"SECRET\"}\n"
	q.Write([]byte(line))
	if len(events) != 0 {
		t.Fatal("startup emitted turn event")
	}
	q.begin()
	for _, s := range []string{line[:40], line[40:]} {
		q.Write([]byte(s))
	}
	q.Write([]byte(line))
	if len(events) != 1 || events[0].Progress.QueuePosition != 482 {
		t.Fatal(events)
	}
	q.mu.Lock()
	q.last = time.Now().Add(-11 * time.Second)
	q.mu.Unlock()
	q.Write([]byte(strings.Replace(line, "482", "452", 1)))
	q.end()
	q.Write([]byte(line))
	if len(events) != 2 {
		t.Fatal(events)
	}
	q.begin()
	q.Write([]byte(strings.Replace(line, "482", "-2", 1)))
	q.Write([]byte(strings.Replace(line, "482", "null", 1)))
	q.Write([]byte(strings.Repeat("x", stderrTailLimit*2) + line))
	q.Write([]byte("ERROR normal diagnostic\n"))
	if len(events) != 2 || strings.Contains(output.String(), "SECRET") || !strings.Contains(output.String(), "normal diagnostic") {
		t.Fatal(output.String(), events)
	}
	data, _ := json.Marshal(events)
	if bytes.Contains(data, []byte("SECRET")) {
		t.Fatal("queue token leaked")
	}
}
func TestQueueDiagnostics_EnvPreservesOtherFilters(t *testing.T) {
	env := queueDiagnosticEnv([]string{"TEST=ok", "RUST_LOG=warn,other=trace,codex_api::sse::trae_chat=off"})
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "other=trace") || strings.Contains(joined, "trae_chat=off") || !strings.Contains(joined, "TEST=ok") {
		t.Fatal(env)
	}
}
func TestACPProgress_PlanAndEmptyCompletionReachTracker(t *testing.T) {
	plan := mapSessionUpdate("s", json.RawMessage(`{"update":{"sessionUpdate":"plan","entries":[{"content":"Fix login","status":"in_progress"},{"content":"Test","status":"pending"}]}}`))
	if len(plan) != 1 || plan[0].Type != core.EventPlan || plan[0].Plan[1].Step != "Test" {
		t.Fatal(plan)
	}
	result := mapSessionUpdate("s", json.RawMessage(`{"update":{"sessionUpdate":"tool_call_update","toolCallId":"t1","status":"completed"}}`))
	if len(result) != 1 || result[0].ToolStatus != "completed" || result[0].ToolID != "t1" {
		t.Fatal(result)
	}
	partial := mapSessionUpdate("s", json.RawMessage(`{"update":{"sessionUpdate":"tool_call_update","toolCallId":"t1","status":"in_progress","content":[{"content":{"text":"partial"}}]}}`))
	if len(partial) != 1 || partial[0].ToolStatus != "in_progress" {
		t.Fatal(partial)
	}
}

func TestACPQueue_StderrEventArrivesBeforeBlockedPromptReturns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := newACPSession(ctx, acpSessionConfig{command: os.Args[0], args: []string{"-test.run=^TestACPQueueHelper$", "--"}, extraEnv: []string{"CC_QUEUE_HELPER=1"}, workDir: t.TempDir(), queueDiagnostics: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	done := make(chan error, 1)
	go func() { done <- s.Send("connectivity only", "", nil, nil) }()
	select {
	case ev := <-s.Events():
		if ev.Type != core.EventProgress || ev.Progress.QueuePosition != 123 {
			t.Fatal(ev)
		}
	case err := <-done:
		t.Fatalf("prompt ended before queue event: %v", err)
	case <-ctx.Done():
		t.Fatal("queue signal blocked behind prompt")
	}
	s.Close()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("close did not release queued request")
	}
	if strings.Contains(s.stderr.String(), "SECRET_QUEUE") {
		t.Fatal("queue token in diagnostic tail")
	}
}
func TestACPQueueHelper(t *testing.T) {
	if os.Getenv("CC_QUEUE_HELPER") != "1" {
		return
	}
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for {
		var req struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		if decoder.Decode(&req) != nil {
			os.Exit(0)
		}
		if req.ID == nil {
			continue
		}
		result := map[string]any{}
		switch req.Method {
		case "initialize":
			result["protocolVersion"] = 1
		case "session/new":
			result["sessionId"] = "queue-test"
		case "session/prompt":
			if !strings.Contains(os.Getenv("RUST_LOG"), "codex_api::sse::trae_chat=debug") {
				os.Exit(1)
			}
			fmt.Fprintln(os.Stderr, `DEBUG codex_api::sse::trae_chat: Trae request_wait_in_queue event: {"position":123,"queue_id":"SECRET_QUEUE"}`)
			continue
		}
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": *req.ID, "result": result})
	}
}

func TestQueueDiagnostics_DoesNotLeakPartialPreviousTurn(t *testing.T) {
	var output bytes.Buffer
	q := &queueDiagnostics{downstream: &output}
	q.begin()
	q.Write([]byte("DEBUG codex_api::sse::trae_chat: " + queueMarker + " {"))
	q.end()
	q.begin()
	q.Write([]byte("\"queue_id\":\"PRIVATE\",\"position\":1}\n"))
	if output.Len() != 0 {
		t.Fatal("partial private diagnostic leaked")
	}
}
