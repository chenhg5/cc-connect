package opencode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestOpencodeSessionUnexpectedExitReportsError(t *testing.T) {
	if os.Getenv("GO_WANT_OPENCODE_EXIT_HELPER") != "" {
		if os.Getenv("GO_WANT_OPENCODE_EXIT_HELPER") == "explicit" {
			_, _ = os.Stdout.WriteString("{\"type\":\"error\",\"error\":\"API failed\"}\n")
			os.Exit(0)
		}
		_, _ = os.Stdout.WriteString("{\"type\":\"step_finish\",\"part\":{\"reason\":\"unknown\"}}\n")
		if os.Getenv("GO_WANT_OPENCODE_EXIT_HELPER") == "nonzero" {
			os.Exit(9)
		}
		os.Exit(0)
	}

	for _, mode := range []string{"clean", "nonzero"} {
		t.Run(mode, func(t *testing.T) {
			s, err := newOpencodeSession(context.Background(), os.Args[0], []string{"-test.run=^TestOpencodeSessionUnexpectedExitReportsError$", "--"}, t.TempDir(), "", "", "", "", []string{"GO_WANT_OPENCODE_EXIT_HELPER=" + mode})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			if err := s.Send("hello", "", nil, nil); err != nil {
				t.Fatal(err)
			}
			select {
			case event := <-s.Events():
				if event.Type != core.EventError || event.Error == nil || !strings.Contains(event.Error.Error(), "without a result") {
					t.Fatalf("abnormal exit event = %+v, want EventError", event)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("no error reported after abnormal exit")
			}
			if s.Alive() {
				t.Fatal("abnormally exited session remains alive")
			}
		})
	}
}

func TestOpencodeSessionExplicitErrorDoesNotDuplicate(t *testing.T) {
	s, err := newOpencodeSession(context.Background(), os.Args[0], []string{"-test.run=^TestOpencodeSessionUnexpectedExitReportsError$", "--"}, t.TempDir(), "", "", "", "", []string{"GO_WANT_OPENCODE_EXIT_HELPER=explicit"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.Send("hello", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	s.wg.Wait()
	if len(s.Events()) != 1 {
		t.Fatalf("explicit error produced %d events, want one", len(s.Events()))
	}
	event := <-s.Events()
	if event.Type != core.EventError || event.Error == nil || !strings.Contains(event.Error.Error(), "API failed") {
		t.Fatalf("explicit error event = %+v", event)
	}
}

func TestOpencodeSessionCloseDoesNotReportExitError(t *testing.T) {
	if os.Getenv("GO_WANT_OPENCODE_CLOSE_HELPER") != "" {
		if err := os.WriteFile(os.Getenv("OPENCODE_CLOSE_READY"), []byte("ready"), 0600); err != nil {
			os.Exit(2)
		}
		time.Sleep(10 * time.Second)
		os.Exit(0)
	}

	ready := filepath.Join(t.TempDir(), "ready")
	s, err := newOpencodeSession(context.Background(), os.Args[0], []string{"-test.run=^TestOpencodeSessionCloseDoesNotReportExitError$", "--"}, t.TempDir(), "", "", "", "", []string{"GO_WANT_OPENCODE_CLOSE_HELPER=1", "OPENCODE_CLOSE_READY=" + ready})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send("hello", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper process did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for event := range s.Events() {
		t.Fatalf("intentional close emitted event: %+v", event)
	}
}
