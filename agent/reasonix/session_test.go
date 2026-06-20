package reasonix

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── Mock reasonix serve ──────────────────────────────────────────

// mockReasonixServe returns an httptest.Server that handles the endpoints
// required by the reasonix agent adapter.
func mockReasonixServe(t *testing.T) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /submit", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusAccepted)
		// turn processing is async in real serve; tests feed SSE separately
	})
	mux.HandleFunc("POST /new", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /approve", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(http.StatusUnsupportedMediaType)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /compact", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		// By default, keep the connection open with no events.
		// Individual tests override this by using a custom handler.
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if ok {
			flusher.Flush()
		}
		<-r.Context().Done() // block until client disconnects
	})

	return httptest.NewServer(mux)
}

// sseServer returns an httptest.Server plus a channel for feeding SSE data.
// Each string sent to the channel is written as `data: <payload>\n\n`.
func sseServer(t *testing.T) (*httptest.Server, chan string) {
	t.Helper()
	ch := make(chan string, 64)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /submit", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("POST /new", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /approve", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}
		flusher.Flush()
		for {
			select {
			case data, more := <-ch:
				if !more {
					return
				}
				_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			case <-r.Context().Done():
				return
			}
		}
	})

	return httptest.NewServer(mux), ch
}

// drainEvents reads up to `limit` events from the channel, blocking up to `timeout`.
func drainEvents(ch <-chan core.Event, limit int, timeout time.Duration) []core.Event {
	var events []core.Event
	deadline := time.After(timeout)
	for i := 0; i < limit; i++ {
		select {
		case evt, ok := <-ch:
			if !ok {
				return events
			}
			events = append(events, evt)
		case <-deadline:
			return events
		}
	}
	return events
}

// mustJSON marshals v to JSON string; panics on error (test helper).
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// ── Test: Send posts to /submit and awaits turn_done ────────────

func TestReasonixSession_Send_PostsToSubmitEndpoint(t *testing.T) {
	var (
		submitMethod string
		submitBody   string
		submitCalled atomic.Bool
		mu           sync.Mutex
	)

	ts, sseCh := sseServer(t)
	defer ts.Close()

	// Override /submit to record the request
	ts.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/submit":
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			submitMethod = r.Method
			submitBody, _ = body["input"].(string)
			mu.Unlock()
			submitCalled.Store(true)
			w.WriteHeader(http.StatusAccepted)
		case "/new":
			w.WriteHeader(http.StatusNoContent)
		case "/events":
			w.WriteHeader(http.StatusOK)
			flusher, _ := w.(http.Flusher)
			flusher.Flush()
			select {
			case data := <-sseCh:
				_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			case <-time.After(5 * time.Second):
			}
			<-r.Context().Done()
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	sess, err := newSession(context.Background(), ts.URL, ".", "test", "default")
	require.NoError(t, err)
	defer func() { _ = sess.Close() }()

	// Feed turn_done so Send() can return
	go func() {
		sseCh <- mustJSON(wireEvent{Kind: "turn_done"})
	}()

	err = sess.Send("hello world", nil, nil)
	assert.NoError(t, err)
	assert.True(t, submitCalled.Load())
	assert.Equal(t, "POST", submitMethod)

	mu.Lock()
	assert.Equal(t, "hello world", submitBody)
	mu.Unlock()
}

// ── Test: SSE event type mapping ─────────────────────────────────

func TestReasonixSession_SSE_MapsEventTypes(t *testing.T) {
	ts, sseCh := sseServer(t)
	defer ts.Close()

	sess, err := newSession(context.Background(), ts.URL, ".", "test", "default")
	require.NoError(t, err)
	defer func() { _ = sess.Close() }()

	// Feed a complete SSE stream
	go func() {
		sseCh <- mustJSON(wireEvent{Kind: "turn_started"})
		sseCh <- mustJSON(wireEvent{Kind: "reasoning", Reasoning: "thinking step by step"})
		sseCh <- mustJSON(wireEvent{Kind: "text", Text: "Here is the answer."})
		sseCh <- mustJSON(wireEvent{Kind: "tool_dispatch", Tool: &wireTool{Name: "read_file", Args: "main.go"}})
		sseCh <- mustJSON(wireEvent{Kind: "tool_result", Tool: &wireTool{Name: "read_file", Output: "package main"}})
		sseCh <- mustJSON(wireEvent{Kind: "text", Text: "Final response."})
		sseCh <- mustJSON(wireEvent{Kind: "turn_done"})
	}()

	// Send triggers SSE consumption
	go func() {
		_ = sess.Send("test", nil, nil)
	}()

	// Drain events
	events := drainEvents(sess.Events(), 6, 5*time.Second)

	// We should see: thinking (flushed at text), text, tool_use, tool_result, text, result
	var types []core.EventType
	for _, evt := range events {
		types = append(types, evt.Type)
	}

	assert.Contains(t, types, core.EventThinking, "should contain thinking event (flushed from accumulator)")
	assert.Contains(t, types, core.EventText, "should contain text event")
	assert.Contains(t, types, core.EventToolUse, "should contain tool_use event")
	assert.Contains(t, types, core.EventToolResult, "should contain tool_result event")
}

// ── Test: SSE reconnect with backoff ─────────────────────────────

func TestReasonixSession_SSEReconnect_Backoff(t *testing.T) {
	var (
		connCount atomic.Int32
		mu        sync.Mutex
		dropConn  = true // first connection will be dropped to simulate crash
		sseFeed   = make(chan string, 64)
	)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /submit", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("POST /new", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		connCount.Add(1)
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		flusher.Flush()

		mu.Lock()
		drop := dropConn
		mu.Unlock()

		if drop {
			// Simulate crash: close connection immediately
			return
		}

		// Normal operation: feed events from channel
		for {
			select {
			case data := <-sseFeed:
				_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			case <-r.Context().Done():
				return
			}
		}
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	sess, err := newSession(context.Background(), ts.URL, ".", "test", "default")
	require.NoError(t, err)
	defer func() { _ = sess.Close() }()

	// Wait for first connection (which will be dropped)
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, int32(1), connCount.Load(), "first SSE connection established")

	// Allow reconnections to succeed
	mu.Lock()
	dropConn = false
	mu.Unlock()

	// Wait for reconnection — should see connCount go to 2
	deadline := time.After(5 * time.Second)
	for connCount.Load() < 2 {
		select {
		case <-time.After(100 * time.Millisecond):
		case <-deadline:
			t.Fatal("timeout waiting for SSE reconnection")
		}
	}
	assert.GreaterOrEqual(t, connCount.Load(), int32(2), "SSE should reconnect after disconnect")
}

// ── Test: Close is idempotent and cancels the read loop ─────────

func TestReasonixSession_Close_CancelsContext(t *testing.T) {
	ts := mockReasonixServe(t)
	defer ts.Close()

	sess, err := newSession(context.Background(), ts.URL, ".", "test", "default")
	require.NoError(t, err)

	// First Close
	err = sess.Close()
	assert.NoError(t, err)
	assert.False(t, sess.Alive())

	// Second Close — must be idempotent (no panic)
	err = sess.Close()
	assert.NoError(t, err)
}

// ── Test: Permission modes ───────────────────────────────────────

func TestReasonixAgent_PermissionMode(t *testing.T) {
	ts := mockReasonixServe(t)
	defer ts.Close()

	tests := []struct {
		input string
		want  string
	}{
		{input: "default", want: "default"},
		{input: "yolo", want: "yolo"},
		{input: "YOLO", want: "yolo"},
		{input: "plan", want: "plan"},
		{input: "auto", want: "yolo"},
		{input: "force", want: "yolo"},
		{input: "unknown", want: "default"},
		{input: "", want: "default"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			agent, err := New(map[string]any{
				"serve_url": ts.URL,
				"mode":      tc.input,
			})
			require.NoError(t, err)

			a, ok := agent.(*Agent)
			require.True(t, ok)
			assert.Equal(t, tc.want, a.GetMode())
		})
	}
}

// ── Test: static interface assertions ────────────────────────────

// These compile-time checks ensure Agent and reasonixSession remain compliant
// with core.Agent and core.AgentSession respectively.
var _ core.Agent = (*Agent)(nil)
var _ core.AgentSession = (*reasonixSession)(nil)

// ── Test: respond permission posts to /approve ───────────────────

func TestReasonixSession_RespondPermission_PostsApprove(t *testing.T) {
	var approveCalls atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("POST /submit", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("POST /new", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /approve", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") == "application/json" {
			approveCalls.Add(1)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		flusher.Flush()
		<-r.Context().Done()
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	sess, err := newSession(context.Background(), ts.URL, ".", "test", "default")
	require.NoError(t, err)
	defer func() { _ = sess.Close() }()

	err = sess.RespondPermission("req-1", core.PermissionResult{Behavior: "allow"})
	assert.NoError(t, err)
	assert.Equal(t, int32(1), approveCalls.Load())

	err = sess.RespondPermission("req-2", core.PermissionResult{Behavior: "deny"})
	assert.NoError(t, err)
	assert.Equal(t, int32(2), approveCalls.Load())
}

// ── Test: compact forwards to POST /compact ──────────────────────

func TestReasonixSession_Send_CompactCommand(t *testing.T) {
	var compactCalled atomic.Bool

	mux := http.NewServeMux()
	mux.HandleFunc("POST /submit", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("POST /new", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /compact", func(w http.ResponseWriter, r *http.Request) {
		compactCalled.Store(true)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		flusher.Flush()
		<-r.Context().Done()
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	sess, err := newSession(context.Background(), ts.URL, ".", "test", "default")
	require.NoError(t, err)
	defer func() { _ = sess.Close() }()

	err = sess.Send("/compact", nil, nil)
	assert.NoError(t, err)
	assert.True(t, compactCalled.Load(), "/compact should forward to POST /compact")
}

// ── Test: httpPost includes response body on error ───────────────

func TestReasonixSession_httpPost_ErrorIncludesBody(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /submit", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		// Write an error body that should be included in the error message
		_, _ = w.Write([]byte(`{"error": "something went wrong"}`))
	})
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		flusher.Flush()
		<-r.Context().Done()
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	sess, err := newSession(context.Background(), ts.URL, ".", "test", "default")
	require.NoError(t, err)
	defer func() { _ = sess.Close() }()

	err = sess.httpPost("/submit", map[string]string{"input": "test"})
	require.Error(t, err)
	errStr := err.Error()
	assert.Contains(t, errStr, "500", "error should include status code")
	assert.Contains(t, errStr, "something went wrong", "error should include response body")
}

// ── Test: Send fails gracefully when session is closed ───────────

func TestReasonixSession_Send_FailsAfterClose(t *testing.T) {
	ts := mockReasonixServe(t)
	defer ts.Close()

	sess, err := newSession(context.Background(), ts.URL, ".", "test", "default")
	require.NoError(t, err)

	err = sess.Close()
	require.NoError(t, err)

	err = sess.Send("hello", nil, nil)
	assert.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "closed") || strings.Contains(err.Error(), "send after close"),
		"error should mention session is closed")
}

// ── Test: thinking accumulator flushes correctly ─────────────────

func TestReasonixSession_ThinkingAccumulator(t *testing.T) {
	ts, sseCh := sseServer(t)
	defer ts.Close()

	sess, err := newSession(context.Background(), ts.URL, ".", "test", "default")
	require.NoError(t, err)
	defer func() { _ = sess.Close() }()

	// Feed reasoning + text + turn_done
	go func() {
		sseCh <- mustJSON(wireEvent{Kind: "reasoning", Reasoning: "Let me"})
		sseCh <- mustJSON(wireEvent{Kind: "reasoning", Reasoning: " think"})
		sseCh <- mustJSON(wireEvent{Kind: "reasoning", Reasoning: " about this."})
		sseCh <- mustJSON(wireEvent{Kind: "text", Text: "Done."})
		sseCh <- mustJSON(wireEvent{Kind: "turn_done"})
	}()

	go func() { _ = sess.Send("test", nil, nil) }()

	events := drainEvents(sess.Events(), 5, 5*time.Second)

	// Find the thinking event
	var thinkingContent string
	for _, evt := range events {
		if evt.Type == core.EventThinking {
			thinkingContent = evt.Content
		}
	}
	assert.Equal(t, "Let me think about this.", thinkingContent,
		"thinking accumulator should aggregate reasoning chunks before text")
}
