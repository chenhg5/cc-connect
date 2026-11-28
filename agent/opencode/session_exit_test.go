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
		mode := os.Getenv("GO_WANT_OPENCODE_EXIT_HELPER")
		if mode == "explicit" {
			_, _ = os.Stdout.WriteString("{\"type\":\"error\",\"error\":\"API failed\"}\n")
			os.Exit(0)
		}
		if mode == "empty-stop" {
			_, _ = os.Stdout.WriteString("{\"type\":\"step_finish\",\"part\":{\"reason\":\"stop\"}}\n")
			os.Exit(0)
		}
		if strings.HasPrefix(mode, "compaction-") {
			_, _ = os.Stdout.WriteString("{\"type\":\"text\",\"part\":{\"text\":\"internal marker\",\"synthetic\":true,\"metadata\":{\"compaction_continue\":true}}}\n")
			if mode == "compaction-resumed" {
				_, _ = os.Stdout.WriteString("{\"type\":\"step_start\",\"sessionID\":\"session-resumed\"}\n")
				_, _ = os.Stdout.WriteString("{\"type\":\"text\",\"part\":{\"text\":\"continued answer\"}}\n")
			}
			_, _ = os.Stdout.WriteString("{\"type\":\"step_finish\",\"part\":{\"reason\":\"stop\"}}\n")
			os.Exit(0)
		}
		if strings.HasPrefix(mode, "text-") {
			_, _ = os.Stdout.WriteString("{\"type\":\"text\",\"part\":{\"text\":\"answer\"}}\n")
			reason := strings.TrimPrefix(mode, "text-")
			if reason == "nonzero" {
				os.Exit(9)
			}
			if reason == "stderr" {
				_, _ = os.Stderr.WriteString("harmless warning\n")
				reason = "unknown"
			}
			if reason == "missing" {
				_, _ = os.Stdout.WriteString("{\"type\":\"step_finish\",\"part\":{}}\n")
			} else {
				_, _ = os.Stdout.WriteString("{\"type\":\"step_finish\",\"part\":{\"reason\":\"" + reason + "\"}}\n")
			}
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

func TestOpencodeSessionStopWithoutTextReportsError(t *testing.T) {
	s, err := newOpencodeSession(context.Background(), os.Args[0], []string{"-test.run=^TestOpencodeSessionUnexpectedExitReportsError$", "--"}, t.TempDir(), "", "", "", "", []string{"GO_WANT_OPENCODE_EXIT_HELPER=empty-stop"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.Send("hello", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	s.wg.Wait()
	if len(s.Events()) != 1 {
		t.Fatalf("events = %d, want one error", len(s.Events()))
	}
	event := <-s.Events()
	if event.Type != core.EventError || event.Error == nil {
		t.Fatalf("empty stop event = %+v, want EventError", event)
	}
}

func TestOpencodeSessionNormalExitAfterTextReportsResult(t *testing.T) {
	for _, reason := range []string{"end_turn", "length", "missing", "tool-calls", "unknown", "stderr"} {
		t.Run(reason, func(t *testing.T) {
			s, err := newOpencodeSession(context.Background(), os.Args[0], []string{"-test.run=^TestOpencodeSessionUnexpectedExitReportsError$", "--"}, t.TempDir(), "", "", "", "", []string{"GO_WANT_OPENCODE_EXIT_HELPER=text-" + reason})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			if err := s.Send("hello", "", nil, nil); err != nil {
				t.Fatal(err)
			}
			s.wg.Wait()
			if len(s.Events()) != 2 {
				t.Fatalf("events = %d, want text and result", len(s.Events()))
			}
			text := <-s.Events()
			result := <-s.Events()
			if text.Type != core.EventText || text.Content != "answer" || result.Type != core.EventResult {
				t.Fatalf("events = %+v, %+v", text, result)
			}
			if !s.Alive() {
				t.Fatal("successful fallback closed the reusable session")
			}
			if reason == "unknown" {
				if err := s.Send("second turn", "", nil, nil); err != nil {
					t.Fatalf("second turn after fallback: %v", err)
				}
				s.wg.Wait()
				if len(s.Events()) != 2 {
					t.Fatalf("second turn emitted %d events, want text and result", len(s.Events()))
				}
			}
		})
	}
}

func TestOpencodeSessionNonzeroExitAfterTextReportsError(t *testing.T) {
	s, err := newOpencodeSession(context.Background(), os.Args[0], []string{"-test.run=^TestOpencodeSessionUnexpectedExitReportsError$", "--"}, t.TempDir(), "", "", "", "", []string{"GO_WANT_OPENCODE_EXIT_HELPER=text-nonzero"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.Send("hello", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	s.wg.Wait()
	if len(s.Events()) != 2 {
		t.Fatalf("events = %d, want text and error", len(s.Events()))
	}
	text := <-s.Events()
	result := <-s.Events()
	if text.Type != core.EventText || result.Type != core.EventError || result.Error == nil {
		t.Fatalf("events = %+v, %+v", text, result)
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

// A terminal step is not a process exit. Core may start queued input as soon
// as it receives a result or error, so neither may overtake streamed output
// that the current process produces before it exits.
func TestOpencodeSessionTerminalEventWaitsForProcess(t *testing.T) {
	if mode := os.Getenv("OPENCODE_TERMINAL_HELPER"); mode != "" {
		_, _ = os.Stdout.WriteString("{\"type\":\"text\",\"part\":{\"text\":\"answer\"}}\n")
		if mode == "error" {
			_, _ = os.Stdout.WriteString("{\"type\":\"error\",\"error\":\"agent rejected request\"}\n")
		} else {
			_, _ = os.Stdout.WriteString("{\"type\":\"step_finish\",\"part\":{\"reason\":\"stop\"}}\n")
			_, _ = os.Stdout.WriteString("{\"type\":\"step_finish\",\"part\":{\"reason\":\"stop\"}}\n")
		}
		_, _ = os.Stdout.WriteString("{\"type\":\"text\",\"part\":{\"text\":\"tail\"}}\n")
		for deadline := time.Now().Add(5 * time.Second); ; {
			if _, err := os.Stat(os.Getenv("OPENCODE_TERMINAL_RELEASE")); err == nil {
				break
			}
			if time.Now().After(deadline) {
				os.Exit(2)
			}
			time.Sleep(5 * time.Millisecond)
		}
		if mode == "nonzero" {
			os.Exit(9)
		}
		os.Exit(0)
	}
	for _, mode := range []string{"stop", "error", "nonzero"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			release := filepath.Join(dir, "release")
			s, err := newOpencodeSession(context.Background(), os.Args[0], []string{"-test.run=^TestOpencodeSessionTerminalEventWaitsForProcess$", "--"}, dir, "", "", "", "", []string{"OPENCODE_TERMINAL_HELPER=" + mode, "OPENCODE_TERMINAL_RELEASE=" + release})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			if err := s.Send("hello", "", nil, nil); err != nil {
				t.Fatal(err)
			}
			next := func() core.Event {
				t.Helper()
				select {
				case event := <-s.Events():
					return event
				case <-time.After(5 * time.Second):
					t.Fatal("timed out waiting for OpenCode event")
					return core.Event{}
				}
			}
			for _, want := range []string{"answer", "tail"} {
				if event := next(); event.Type != core.EventText || event.Content != want {
					t.Fatalf("event before process release = %+v, want text %q", event, want)
				}
			}
			select {
			case event := <-s.Events():
				t.Fatalf("unexpected event before process release: %+v", event)
			default:
			}
			if err := os.WriteFile(release, nil, 0600); err != nil {
				t.Fatal(err)
			}
			event := next()
			if mode == "stop" {
				if event.Type != core.EventResult || !event.Done {
					t.Fatalf("terminal event: %+v", event)
				}
			} else {
				want := "exit status 9"
				if mode == "error" {
					want = "agent rejected request"
				}
				if event.Type != core.EventError || event.Error == nil || !strings.Contains(event.Error.Error(), want) {
					t.Fatalf("terminal event: %+v, want error containing %q", event, want)
				}
			}
			s.wg.Wait()
			if len(s.Events()) != 0 {
				t.Fatal("duplicate terminal event")
			}
		})
	}
}

func TestOpencodeSessionCompactionCompletion(t *testing.T) {
	for _, mode := range []string{"compaction-pending", "compaction-resumed"} {
		t.Run(mode, func(t *testing.T) {
			s, err := newOpencodeSession(context.Background(), os.Args[0], []string{"-test.run=^TestOpencodeSessionUnexpectedExitReportsError$", "--"}, t.TempDir(), "", "", "", "", []string{"GO_WANT_OPENCODE_EXIT_HELPER=" + mode})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			if err := s.Send("hello", "", nil, nil); err != nil {
				t.Fatal(err)
			}
			s.wg.Wait()
			if mode == "compaction-pending" {
				if len(s.Events()) != 0 {
					t.Fatal("internal continuation emitted events")
				}
				return
			}
			if len(s.Events()) != 2 {
				t.Fatalf("events = %d, want text and result", len(s.Events()))
			}
			answer, result := <-s.Events(), <-s.Events()
			if answer.Type != core.EventText || answer.Content != "continued answer" ||
				result.Type != core.EventResult || result.SessionID != "session-resumed" {
				t.Fatalf("events = %+v, %+v", answer, result)
			}
		})
	}
}
