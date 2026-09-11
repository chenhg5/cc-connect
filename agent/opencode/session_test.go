package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// TestOpencodeSessionEntry_Unmarshal verifies that OpenCode's
// `session list --format json` output can be correctly parsed.
//
// OpenCode returns `updated` and `created` as Unix timestamps in
// milliseconds (int64), not strings. This test prevents regression
// of the unmarshal error:
//
//	json: cannot unmarshal number into Go struct field opencodeSessionEntry.updated of type string
func TestOpencodeSessionEntry_Unmarshal(t *testing.T) {
	jsonData := `[
  {
    "id": "ses_2eb11bb11ffeYwQZOj25mlmGMc",
    "title": "Test Session",
    "updated": 1774174646445,
    "created": 1774172652782,
    "projectId": "b80385ead03e8b450bdb2016d434aad318f93c16",
    "directory": "/path/to/project"
  }
]`

	var entries []opencodeSessionEntry
	if err := json.Unmarshal([]byte(jsonData), &entries); err != nil {
		t.Fatalf("Failed to unmarshal OpenCode session list: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("Expected 1 entry, got %d", len(entries))
	}

	e := entries[0]
	if e.ID != "ses_2eb11bb11ffeYwQZOj25mlmGMc" {
		t.Errorf("ID = %q, want %q", e.ID, "ses_2eb11bb11ffeYwQZOj25mlmGMc")
	}
	if e.Title != "Test Session" {
		t.Errorf("Title = %q, want %q", e.Title, "Test Session")
	}
	if e.Updated != 1774174646445 {
		t.Errorf("Updated = %d, want %d", e.Updated, 1774174646445)
	}
	if e.Created != 1774172652782 {
		t.Errorf("Created = %d, want %d", e.Created, 1774172652782)
	}
}

// TestNewOpencodeSession_ContinueSessionTreatedAsFresh verifies that
// the ContinueSession sentinel (__continue__) is not passed as a literal
// session ID to the CLI. This was fixed in PR #249.
func TestNewOpencodeSession_ContinueSessionTreatedAsFresh(t *testing.T) {
	s, err := newOpencodeSession(context.Background(), "echo", nil, "/tmp", "", "default", "", core.ContinueSession, nil)
	if err != nil {
		t.Fatalf("newOpencodeSession: %v", err)
	}
	defer s.Close()

	if got := s.CurrentSessionID(); got != "" {
		t.Errorf("ContinueSession should be treated as fresh: chatID = %q, want empty", got)
	}
}

func TestOpencodeSessionStageImages(t *testing.T) {
	dir := t.TempDir()
	s := &opencodeSession{workDir: dir}

	prompt, imagePaths, err := s.stageImages("", []core.ImageAttachment{
		{MimeType: "image/jpeg", Data: []byte{0xff, 0xd8, 0xff}},
		{MimeType: "image/webp", Data: []byte("webp")},
	})
	if err != nil {
		t.Fatalf("stageImages: %v", err)
	}
	if prompt != "Please analyze the attached image(s)." {
		t.Fatalf("prompt = %q", prompt)
	}
	if len(imagePaths) != 2 {
		t.Fatalf("imagePaths len = %d, want 2", len(imagePaths))
	}
	if filepath.Ext(imagePaths[0]) != ".jpg" {
		t.Fatalf("first ext = %q, want .jpg", filepath.Ext(imagePaths[0]))
	}
	if filepath.Ext(imagePaths[1]) != ".webp" {
		t.Fatalf("second ext = %q, want .webp", filepath.Ext(imagePaths[1]))
	}
	for _, path := range imagePaths {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected staged image %s: %v", path, err)
		}
	}
}

func TestOpencodeSessionBuildRunArgsIncludesImagesAsFiles(t *testing.T) {
	s := &opencodeSession{workDir: "/repo", model: "provider/model"}

	got := s.buildRunArgs("describe these images", []string{"/tmp/a.png", "/tmp/b.jpg"}, "ses_123")
	want := []string{
		"run", "--format", "json",
		"--session", "ses_123",
		"--model", "provider/model",
		"--dir", "/repo",
		"--thinking",
		"--file", "/tmp/a.png",
		"--file", "/tmp/b.jpg",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

// TestHandleStepStart_SessionIDFromTopLevel verifies that handleStepStart
// prefers the sessionID from the top-level JSON field when both top-level
// and part-level sessionID are present. This matches OpenCode's stdout format.
func TestHandleStepStart_SessionIDFromTopLevel(t *testing.T) {
	jsonData := `{"type":"step_start","sessionID":"ses_top_level","part":{"sessionID":"ses_part_level"}}`

	var raw map[string]any
	if err := json.Unmarshal([]byte(jsonData), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	s := &opencodeSession{}
	s.handleStepStart(raw)

	if got := s.CurrentSessionID(); got != "ses_top_level" {
		t.Errorf("sessionID = %q, want %q (should prefer top-level)", got, "ses_top_level")
	}
}

// TestHandleStepStart_SessionIDFromPart verifies that handleStepStart
// falls back to the sessionID inside part when top-level sessionID is absent.
func TestHandleStepStart_SessionIDFromPart(t *testing.T) {
	jsonData := `{"type":"step_start","part":{"sessionID":"ses_part_level"}}`

	var raw map[string]any
	if err := json.Unmarshal([]byte(jsonData), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	s := &opencodeSession{}
	s.handleStepStart(raw)

	if got := s.CurrentSessionID(); got != "ses_part_level" {
		t.Errorf("sessionID = %q, want %q (should fallback to part)", got, "ses_part_level")
	}
}

// TestHandleStepStopSendsEventResult verifies that handleStepFinish sends
// an EventResult when reason="stop", signaling turn completion to the engine.
func TestHandleStepStopSendsEventResult(t *testing.T) {
	jsonData := `{"type":"step_finish","part":{"reason":"stop"}}`

	var raw map[string]any
	if err := json.Unmarshal([]byte(jsonData), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &opencodeSession{events: make(chan core.Event, 1), ctx: ctx}
	s.handleStepFinish(raw, s.turnGen.Load())

	select {
	case evt := <-s.events:
		if evt.Type != core.EventResult {
			t.Errorf("event type = %q, want EventResult", evt.Type)
		}
		if !evt.Done {
			t.Errorf("event.Done = false, want true")
		}
	default:
		t.Error("expected EventResult to be sent when reason=stop")
	}
}

// TestHandleStepToolCallsNoEventResult verifies that handleStepFinish does NOT
// send EventResult when reason="tool-calls", allowing the agent to continue
// with subsequent tool execution steps.
func TestHandleStepToolCallsNoEventResult(t *testing.T) {
	jsonData := `{"type":"step_finish","part":{"reason":"tool-calls"}}`

	var raw map[string]any
	if err := json.Unmarshal([]byte(jsonData), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &opencodeSession{events: make(chan core.Event, 1), ctx: ctx}
	s.handleStepFinish(raw, s.turnGen.Load())

	select {
	case evt := <-s.events:
		t.Errorf("unexpected event sent when reason=tool-calls: %v", evt)
	default:
	}
}

// TestHandleStepDuplicateEventResultPrevented verifies that calling
// handleStepFinish multiple times with reason="stop" only sends one
// EventResult, preventing duplicate completion signals to the engine.
func TestHandleStepDuplicateEventResultPrevented(t *testing.T) {
	jsonData := `{"type":"step_finish","part":{"reason":"stop"}}`

	var raw map[string]any
	if err := json.Unmarshal([]byte(jsonData), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &opencodeSession{
		events:     make(chan core.Event, 2),
		ctx:        ctx,
		resultSent: atomic.Bool{},
	}

	s.handleStepFinish(raw, s.turnGen.Load())
	s.handleStepFinish(raw, s.turnGen.Load())

	count := 0
	for len(s.events) > 0 {
		evt := <-s.events
		if evt.Type == core.EventResult {
			count++
		}
	}

	if count != 1 {
		t.Errorf("EventResult count = %d, want 1 (duplicate should be prevented)", count)
	}
}

// TestHandleToolUsePermissionDeniedEmitsEventText verifies that when opencode
// rejects a tool call (status="error"), the error message is emitted as an
// EventText so the engine has something meaningful to deliver instead of
// the generic "(空响应)" / "(empty response)" placeholder.
// Reproduces the scenario in issue #178 where running bash commands in
// default mode silently produced an empty response.
func TestHandleToolUsePermissionDeniedEmitsEventText(t *testing.T) {
	jsonData := `{"type":"tool_use","part":{"tool":"bash","state":{"status":"error","error":"The user rejected permission to use this specific tool call.","input":{"command":"ls","description":"List files in current directory"}}}}`

	var raw map[string]any
	if err := json.Unmarshal([]byte(jsonData), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &opencodeSession{events: make(chan core.Event, 4), ctx: ctx}
	s.handleToolUse(raw)

	var events []core.Event
	for len(s.events) > 0 {
		events = append(events, <-s.events)
	}

	if len(events) < 2 {
		t.Fatalf("expected at least 2 events (EventToolUse + EventText), got %d: %v", len(events), events)
	}
	if events[0].Type != core.EventToolUse {
		t.Errorf("events[0].Type = %v, want EventToolUse", events[0].Type)
	}
	if events[1].Type != core.EventText {
		t.Errorf("events[1].Type = %v, want EventText (error text so engine has content)", events[1].Type)
	}
	if !strings.Contains(events[1].Content, "rejected permission") {
		t.Errorf("EventText.Content = %q, want it to contain the rejection reason", events[1].Content)
	}
}

// TestHandleToolUseCompletedDoesNotEmitExtraText verifies that a successfully
// completed tool call does NOT emit an EventText (regression guard).
func TestHandleToolUseCompletedDoesNotEmitExtraText(t *testing.T) {
	jsonData := `{"type":"tool_use","part":{"tool":"bash","state":{"status":"completed","output":"file1.txt file2.txt","input":{"command":"ls"}}}}`

	var raw map[string]any
	if err := json.Unmarshal([]byte(jsonData), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &opencodeSession{events: make(chan core.Event, 4), ctx: ctx}
	s.handleToolUse(raw)

	var events []core.Event
	for len(s.events) > 0 {
		events = append(events, <-s.events)
	}

	for _, evt := range events {
		if evt.Type == core.EventText {
			t.Errorf("unexpected EventText for completed tool: %q", evt.Content)
		}
	}
	if len(events) < 2 {
		t.Errorf("expected EventToolUse + EventToolResult for completed tool, got %d events", len(events))
	}
}

// TestHandleToolUseErrorNoMessageNoText verifies that a tool error with empty
// error message does NOT emit a spurious empty EventText.
func TestHandleToolUseErrorNoMessageNoText(t *testing.T) {
	jsonData := `{"type":"tool_use","part":{"tool":"bash","state":{"status":"error"}}}`

	var raw map[string]any
	if err := json.Unmarshal([]byte(jsonData), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &opencodeSession{events: make(chan core.Event, 4), ctx: ctx}
	s.handleToolUse(raw)

	for len(s.events) > 0 {
		evt := <-s.events
		if evt.Type == core.EventText {
			t.Errorf("unexpected EventText for error with no message: %q", evt.Content)
		}
	}
}

// TestStaleFallbackEventResultSuppressed reproduces the "(empty response)"
// bug for the first message dequeued from a busy-session queue.
//
// Sequence (as observed in production):
//  1. Turn 1's process emits step_finish reason=stop → EventResult. The
//     engine consumes it and immediately dequeues the next queued message.
//  2. The engine calls Send() for turn 2, which resets resultSent and spawns
//     a new process. Turn 1's process is still alive — its readLoop lingers
//     until the process exits, typically hundreds of milliseconds later.
//  3. Turn 1's process exits. Its readLoop's EOF fallback calls
//     sendEventResult. Because resultSent was reset by turn 2's Send, the
//     old guard let a stale EventResult through, terminating turn 2 before
//     any of its output arrived → "(空响应)" placeholder.
//
// With the turn-generation guard, step 3 must emit nothing.
func TestStaleFallbackEventResultSuppressed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &opencodeSession{events: make(chan core.Event, 4), ctx: ctx}

	// Turn 1 begins (mimic Send's per-turn reset).
	s.resultSent.Store(false)
	gen1 := s.turnGen.Add(1)

	// Turn 1's process emits step_finish reason=stop.
	s.sendEventResult(gen1)
	evt := <-s.events
	if evt.Type != core.EventResult {
		t.Fatalf("turn 1: event type = %q, want EventResult", evt.Type)
	}

	// Engine dequeues the queued message and calls Send for turn 2.
	s.resultSent.Store(false)
	gen2 := s.turnGen.Add(1)

	// Turn 1's process exits now; its readLoop EOF fallback fires with gen1.
	s.sendEventResult(gen1)
	select {
	case evt := <-s.events:
		t.Fatalf("stale EventResult from turn 1's process leaked into turn 2: %+v", evt)
	default:
	}

	// Turn 2's own completion must still go through.
	s.sendEventResult(gen2)
	select {
	case evt := <-s.events:
		if evt.Type != core.EventResult {
			t.Fatalf("turn 2: event type = %q, want EventResult", evt.Type)
		}
	default:
		t.Fatal("turn 2's own EventResult was not sent")
	}
}

// TestQueuedTurnNotTerminatedByLingeringPreviousProcess is an integration
// repro of the same bug through the real Send/readLoop path, using this test
// binary as a fake agent CLI (see TestHelperProcess).
//
// Turn 1's fake process emits its final event, then lingers 400ms before
// exiting — like the real CLI, which flushes/tears down after printing the
// result. Turn 2 is sent as soon as turn 1's EventResult is consumed
// (exactly what the engine's queue-drain path does), and its fake process
// takes 700ms to produce output. Without the fix, turn 1's exit produces a
// stale EventResult at ~400ms and turn 2 completes "empty".
func TestQueuedTurnNotTerminatedByLingeringPreviousProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s, err := newOpencodeSession(ctx, os.Args[0],
		[]string{"-test.run=TestHelperProcess", "--"},
		t.TempDir(), "", "default", "", "", []string{"GO_OC_HELPER=1"})
	if err != nil {
		t.Fatalf("newOpencodeSession: %v", err)
	}
	defer func() { _ = s.Close() }()

	if err := s.Send("turn1", "msg1", nil, nil); err != nil {
		t.Fatalf("Send turn1: %v", err)
	}
	waitForResult(t, s, "turn 1", nil)

	// Engine behavior after EventResult: dequeue and Send immediately,
	// while turn 1's process is still lingering.
	if err := s.Send("turn2", "msg2", nil, nil); err != nil {
		t.Fatalf("Send turn2: %v", err)
	}
	var texts []string
	waitForResult(t, s, "turn 2", &texts)

	joined := strings.Join(texts, "")
	if !strings.Contains(joined, "reply two") {
		t.Fatalf("turn 2 completed without its reply text (stale EventResult ended the turn early); got text %q", joined)
	}
}

// waitForResult consumes events until EventResult, collecting EventText
// content into texts if non-nil. Fails the test on EventError or timeout.
func waitForResult(t *testing.T, s *opencodeSession, label string, texts *[]string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case evt := <-s.events:
			switch evt.Type {
			case core.EventResult:
				return
			case core.EventError:
				t.Fatalf("%s: unexpected EventError: %v", label, evt.Error)
			case core.EventText:
				if texts != nil {
					*texts = append(*texts, evt.Content)
				}
			}
		case <-deadline:
			t.Fatalf("%s: timed out waiting for EventResult", label)
		}
	}
}

// TestHelperProcess acts as a fake agent CLI for the integration test above.
// It is only active when GO_OC_HELPER=1; the prompt arrives on stdin.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_OC_HELPER") != "1" {
		return
	}
	defer os.Exit(0)
	in, _ := io.ReadAll(os.Stdin)
	prompt := string(in)
	switch {
	case strings.Contains(prompt, "turn1"):
		fmt.Println(`{"type":"step_start","sessionID":"ses_helper"}`)
		fmt.Println(`{"type":"text","part":{"text":"reply one"}}`)
		fmt.Println(`{"type":"step_finish","part":{"reason":"stop"}}`)
		_ = os.Stdout.Sync()
		// Linger after the final event, like the real CLI's teardown.
		time.Sleep(400 * time.Millisecond)
	case strings.Contains(prompt, "turn2"):
		// Simulate LLM latency before any output.
		time.Sleep(700 * time.Millisecond)
		fmt.Println(`{"type":"text","part":{"text":"reply two"}}`)
		fmt.Println(`{"type":"step_finish","part":{"reason":"stop"}}`)
	}
}

// verify Agent implements core.Agent
var _ core.Agent = (*Agent)(nil)
