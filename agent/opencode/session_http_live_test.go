package opencode

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestOpencodeHTTPModeLiveServer(t *testing.T) {
	connectionURL := os.Getenv("OPENCODE_LIVE_URL")
	if connectionURL == "" {
		t.Skip("set OPENCODE_LIVE_URL to run against a local opencode serve")
	}

	agent, err := New(map[string]any{
		"work_dir":       t.TempDir(),
		"connection_url": connectionURL,
		"username":       os.Getenv("OPENCODE_LIVE_USERNAME"),
		"password":       os.Getenv("OPENCODE_LIVE_PASSWORD"),
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	if session.CurrentSessionID() == "" {
		t.Fatal("expected live HTTP session ID")
	}
	if os.Getenv("OPENCODE_LIVE_SEND") == "" {
		return
	}

	sendDone := make(chan error, 1)
	startedAt := time.Now()
	go func() {
		sendDone <- session.Send("Reply exactly with CC_CONNECT_LIVE_OK.", nil, nil)
	}()

	var text strings.Builder
	firstTextAt := time.Time{}
	sendDoneAt := time.Time{}
	var sendErr error
	deadline := time.After(2 * time.Minute)
	for {
		select {
		case err := <-sendDone:
			sendDoneAt = time.Now()
			sendErr = err
		case event := <-session.Events():
			switch event.Type {
			case core.EventText:
				if firstTextAt.IsZero() {
					firstTextAt = time.Now()
				}
				text.WriteString(event.Content)
			case core.EventResult:
				if sendDoneAt.IsZero() {
					select {
					case sendErr = <-sendDone:
						sendDoneAt = time.Now()
					case <-time.After(10 * time.Second):
						t.Fatal("timed out waiting for prompt response after EventResult")
					}
				}
				if sendErr != nil {
					t.Fatal(sendErr)
				}
				if firstTextAt.IsZero() {
					t.Fatal("expected at least one streamed text event")
				}
				if !strings.Contains(text.String(), "CC_CONNECT_LIVE_OK") {
					t.Fatalf("live response text = %q, want CC_CONNECT_LIVE_OK", text.String())
				}
				if strings.Contains(text.String(), "Reply exactly") {
					t.Fatalf("live response echoed user prompt: %q", text.String())
				}
				t.Logf("first_text_after=%s prompt_response_after=%s total=%s", firstTextAt.Sub(startedAt), sendDoneAt.Sub(startedAt), time.Since(startedAt))
				return
			case core.EventError:
				t.Fatal(event.Error)
			}
		case <-deadline:
			t.Fatalf("timed out waiting for live response; text so far: %q", text.String())
		}
	}
}
