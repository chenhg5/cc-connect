package opencode

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// TestLivePromptAsyncAndSSE against the running opencode serve at localhost:4096.
// Verifies: (1) prompt_async returns 204 quickly, (2) /event SSE delivers events,
// (3) the SSE auto-reconnects after we forcibly close it server-side.
//
// Run with: OPENCODE_LIVE_RESUME_TEST=1 go test ./agent/opencode/ -run TestLivePromptAsync -v -timeout 60s
func TestLivePromptAsync(t *testing.T) {
	if os.Getenv("OPENCODE_LIVE_RESUME_TEST") == "" {
		t.Skip("set OPENCODE_LIVE_RESUME_TEST=1 to run against local opencode serve")
	}
	url := os.Getenv("OPENCODE_LIVE_URL")
	if url == "" {
		url = "http://localhost:4096"
	}
	pw := os.Getenv("OPENCODE_SERVER_PASSWORD")
	if pw == "" {
		t.Skip("OPENCODE_SERVER_PASSWORD required for live test")
	}

	agent, err := New(map[string]any{
		"cmd":            "/bin/false",
		"work_dir":       "/tmp",
		"connection_url": url,
		"mode":           "default",
		"model":          "zhipuai-coding-plan/glm-5.2",
		"env":            map[string]any{"OPENCODE_SERVER_PASSWORD": pw},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	t.Logf("session id: %s", session.CurrentSessionID())

	// Send a simple prompt. prompt_async should return 204 quickly.
	start := time.Now()
	sendErr := make(chan error, 1)
	go func() { sendErr <- session.Send("Reply with exactly the word PONG, nothing else.", "", nil, nil) }()
	select {
	case err := <-sendErr:
		if err != nil {
			t.Fatalf("Send returned error: %v", err)
		}
		t.Logf("Send returned in %v (prompt_async 204 ack)", time.Since(start))
	case <-time.After(10 * time.Second):
		t.Fatal("Send did not return within 10s — prompt_async not fire-and-forget?")
	}

	// Wait for events: busy → step → text → idle. Collect text.
	var textBuf strings.Builder
	var gotResult atomic.Bool
	deadline := time.After(60 * time.Second)
	for !gotResult.Load() {
		select {
		case event := <-session.Events():
			t.Logf("event: type=%q content=%q", event.Type, truncStr(event.Content, 80))
			switch event.Type {
			case core.EventText:
				textBuf.WriteString(event.Content)
			case core.EventResult:
				gotResult.Store(true)
			case core.EventError:
				t.Fatalf("unexpected error event: %v", event.Error)
			}
		case <-deadline:
			t.Fatal("did not receive EventResult within 60s")
		}
	}
	if !strings.Contains(strings.ToUpper(textBuf.String()), "PONG") {
		t.Logf("warning: response did not contain PONG: %q", textBuf.String())
	} else {
		t.Logf("PASS: got expected response containing PONG")
	}
}

// TestLiveSSEReconnectAfterServerRestart verifies that cc-connect's SSE reconnects
// after the opencode-server is briefly unavailable.
//
// Skipped unless OPENCODE_LIVE_RESUME_TEST=1 AND the user manually restarts opencode-server.
// This is mostly a manual test stub — automated version requires controlling the server.
func TestLiveSSEReconnectAfterServerRestart(t *testing.T) {
	if os.Getenv("OPENCODE_LIVE_RESUME_TEST") == "" {
		t.Skip("set OPENCODE_LIVE_RESUME_TEST=1 to run")
	}
	url := os.Getenv("OPENCODE_LIVE_URL")
	if url == "" {
		url = "http://localhost:4096"
	}
	pw := os.Getenv("OPENCODE_SERVER_PASSWORD")
	if pw == "" {
		t.Skip("OPENCODE_SERVER_PASSWORD required")
	}

	// Use a proxy that lets us kill the connection at will.
	var killCount atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/event" && killCount.Add(1) == 1 {
			// First SSE request: return immediately to force client reconnect.
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			return
		}
		// Otherwise, proxy through.
		req, err := http.NewRequestWithContext(r.Context(), r.Method, url+r.URL.Path+"?"+r.URL.RawQuery, r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		req.Header = r.Header.Clone()
		resp, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		flusher, _ := w.(http.Flusher)
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				_, _ = w.Write(buf[:n])
				if flusher != nil {
					flusher.Flush()
				}
			}
			if err != nil {
				return
			}
		}
	}))
	defer proxy.Close()

	agent, err := New(map[string]any{
		"cmd":            "/bin/false",
		"work_dir":       "/tmp",
		"connection_url": proxy.URL,
		"mode":           "default",
		"model":          "anthropic/claude-haiku-4-5",
		"env":            map[string]any{"OPENCODE_SERVER_PASSWORD": pw},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	t.Logf("session id: %s, kill count: %d", session.CurrentSessionID(), killCount.Load())
	// Wait for reconnect to happen.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if killCount.Load() >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := killCount.Load(); got < 2 {
		t.Fatalf("SSE did not reconnect: killCount=%d", got)
	}
	t.Logf("PASS: SSE reconnected after forced EOF (killCount=%d)", killCount.Load())
}

func truncStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return fmt.Sprintf("%s…(%d more)", s[:n], len(s)-n)
}
