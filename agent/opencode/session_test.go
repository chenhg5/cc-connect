package opencode

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"syscall"
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
	s.handleStepFinish(raw)

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
	s.handleStepFinish(raw)

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

	s.handleStepFinish(raw)
	s.handleStepFinish(raw)

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

// TestOpencodeErrorKind_RetriableOverloaded guards the opencode error
// classification added alongside PR #1641 (which only covered codex): transient
// server failures such as "APIError: Our servers are currently overloaded.
// Please try again later." must map to ErrorKindOverloaded so the engine's
// generic whole-turn retry kicks in instead of failing the turn with a bare
// "❌ 错误:" message.
func TestOpencodeErrorKind_RetriableOverloaded(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    core.ErrorKind
	}{
		{
			name:    "server overloaded (user-reported)",
			message: "APIError: Our servers are currently overloaded. Please try again later.",
			want:    core.ErrorKindOverloaded,
		},
		{
			name:    "at capacity",
			message: "Provider API error: The model is at capacity. Please try again later.",
			want:    core.ErrorKindOverloaded,
		},
		{
			name:    "gateway error",
			message: "unexpected status 503 Service Unavailable, url: https://aiapi.uu.cc/v1/messages",
			want:    core.ErrorKindOverloaded,
		},
		{
			name:    "stream disconnected",
			message: "stream disconnected before completion: An error occurred while processing your request.",
			want:    core.ErrorKindOverloaded,
		},
		{
			name:    "rate limit",
			message: "Provider API error: rate limit exceeded, retry after 30s",
			want:    core.ErrorKindRateLimit,
		},
		{
			name:    "non transient error remains unknown",
			message: "authentication failed: invalid api key",
			want:    core.ErrorKindUnknown,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := opencodeErrorKind(tc.message); got != tc.want {
				t.Fatalf("opencodeErrorKind() = %q, want %q", got, tc.want)
			}
		})
	}
}

// verify Agent implements core.Agent
var _ core.Agent = (*Agent)(nil)

// --- Compaction deadlock regression tests (fix/opencode-empty-progress-card-leak) ---
//
// Background: after an OpenCode auto-compaction the process stops and waits
// for a "continue" prompt on stdin. The old code only resumed on stdout EOF,
// so a process that waited instead of exiting deadlocked the readLoop forever
// (observed live: a turn stuck for 22 minutes until the process was killed).
// The fix kills the waiting process when the compaction event arrives and
// resumes the same session with a fresh "continue" process. These tests pin
// that behavior. They also guard the stdin EOF contract: `opencode run` only
// starts executing after stdin EOF, so the prompt pipe must be closed after
// the write (an open pipe hangs the turn before the first tool call).

func compactionSummaryRaw() map[string]any {
	return map[string]any{
		"type": "text",
		"part": map[string]any{
			"type": "text",
			"text": "## Objective\n\nreproduce the bug\n## Work State\n\nin progress",
		},
	}
}

func compactionContinueRaw() map[string]any {
	return map[string]any{
		"type": "text",
		"part": map[string]any{
			"type":      "text",
			"text":      "",
			"synthetic": true,
			"metadata":  map[string]any{"compaction_continue": true},
		},
	}
}

// startWaitingProcess starts a real process that blocks on stdin (like
// opencode does while waiting between steps) so watchdog tests have a live
// process to kill.
func startWaitingProcess(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sh", "-c", "read line; sleep 30")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Process.Kill() })
	return cmd
}

func setCurrentCmd(t *testing.T, s *opencodeSession, cmd *exec.Cmd) {
	t.Helper()
	s.procMu.Lock()
	s.currentCmd = cmd
	s.procMu.Unlock()
}

func waitKilled(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	select {
	case err := <-waitCh:
		if err == nil {
			t.Error("process should have been killed, Wait returned nil")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("process was not killed")
	}
}

// TestCompactionDoesNotKillProcess is the regression test for the
// compaction-loop bug: OpenCode V2 compaction is checkpoint-based and
// auto-retries the pending step inside the same process, so the bridge must
// NOT kill the process when the compaction summary/compaction_continue event
// arrives. Killing it made the resumed process re-compact every ~30s until
// the continuation limit was hit, ending the turn with an empty 11-char
// reply. The process must stay alive and only the expectingContinue flag is
// set (a later stall is handled by the watchdog, not by an immediate kill).
func TestCompactionDoesNotKillProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &opencodeSession{events: make(chan core.Event, 8), ctx: ctx}

	cmd := startWaitingProcess(t)
	setCurrentCmd(t, s, cmd)

	s.handleEvent(compactionSummaryRaw())

	if !s.expectingContinue.Load() {
		t.Error("expectingContinue should be set after compaction")
	}
	// The process must still be alive: the compaction auto-retry happens
	// inside the same process.
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Errorf("process must NOT be killed by a compaction event: %v", err)
	}

	// A second compaction event from the same round must not re-arm anything
	// and must not kill the process either.
	s.handleEvent(compactionContinueRaw())
	if !s.expectingContinue.Load() {
		t.Error("expectingContinue should stay set after duplicate event")
	}
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Errorf("process must NOT be killed by a duplicate compaction event: %v", err)
	}
}

// TestCompactionWorkEventClearsFlag verifies that a real work event clears
// the expectingContinue flag set by a compaction, so a later stall after
// another compaction round is still detected as compaction-then-exit by
// readLoop (and the watchdog can act on event silence).
func TestCompactionWorkEventClearsFlag(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &opencodeSession{events: make(chan core.Event, 8), ctx: ctx}

	s.handleEvent(compactionSummaryRaw())
	if !s.expectingContinue.Load() {
		t.Fatal("expectingContinue should be set after compaction")
	}

	// The process resumes with a tool call: flag must clear.
	toolRaw := map[string]any{
		"type": "tool_use",
		"part": map[string]any{"type": "tool", "tool": "bash", "state": map[string]any{}},
	}
	s.handleEvent(toolRaw)
	if s.expectingContinue.Load() {
		t.Error("expectingContinue should be cleared by a work event")
	}

	// A later compaction can set the flag again.
	s.handleEvent(compactionContinueRaw())
	if !s.expectingContinue.Load() {
		t.Error("expectingContinue should be set again by a second compaction")
	}
}

// TestStallWatchdogKillsStalledProcess is the regression test for the
// long-hang bug (observed live: a stalled opencode process sat idle for 22
// minutes): when no event arrives for stallTimeout while the turn is not
// finished, the watchdog must kill the process so readLoop sees EOF and ends
// the turn with a notice instead of hanging forever.
func TestStallWatchdogKillsStalledProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &opencodeSession{events: make(chan core.Event, 8), ctx: ctx}
	s.alive.Store(true)
	s.resultSent.Store(false)
	// Simulate a process that has been silent well past the stall timeout.
	s.lastEvent.Store(time.Now().Add(-(stallTimeout + time.Minute)).UnixNano())

	cmd := startWaitingProcess(t)
	setCurrentCmd(t, s, cmd)

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.stallWatchdog(ctx, 10*time.Millisecond)
	}()
	waitKilled(t, cmd)
	cancel()
	<-done
}

// TestStallWatchdogSparesActiveProcess verifies the watchdog does not touch a
// process that is producing events (lastEvent fresh), even if a compaction
// happened earlier.
func TestStallWatchdogSparesActiveProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &opencodeSession{events: make(chan core.Event, 8), ctx: ctx}
	s.alive.Store(true)
	s.resultSent.Store(false)
	s.lastEvent.Store(time.Now().UnixNano()) // fresh activity

	cmd := startWaitingProcess(t)
	setCurrentCmd(t, s, cmd)

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.stallWatchdog(ctx, 10*time.Millisecond)
	}()
	// Give the watchdog several ticks; the process must stay alive.
	time.Sleep(60 * time.Millisecond)
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Errorf("active process must not be killed by the watchdog: %v", err)
	}
	cancel()
	<-done
}


// TestSendResetsStallWatchdogClock is the regression test for the
// watchdog-miskill bug: lastEvent carried over from the previous turn, so the
// stall watchdog saw a multi-minute idle immediately after a new turn started,
// killed the freshly launched process and the turn ended with an empty reply
// (observed live: response_len=11, input_tokens=0, "killing stalled process
// idle=5m3s" one second after the message arrived). Send must reset the
// watchdog clock so each turn gets a full stall window.
func TestSendResetsStallWatchdogClock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := newOpencodeSession(ctx, "true", nil, t.TempDir(), "", "yolo", "", "", nil)
	if err != nil {
		t.Fatalf("newOpencodeSession: %v", err)
	}
	defer func() { _ = s.Close() }()
	s.alive.Store(true)
	// Stale last-event from a previous turn (way past the stall timeout).
	s.lastEvent.Store(time.Now().Add(-time.Hour).UnixNano())

	if err := s.Send("hi", "", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := s.lastEvent.Load(); time.Since(time.Unix(0, got)) > time.Minute {
		t.Errorf("lastEvent not reset by Send: idle=%v, watchdog would miskill the new turn", time.Since(time.Unix(0, got)))
	}
}

// TestStdinPipe_PromptThenEOF pins the stdin EOF contract: after the prompt
// is consumed by the child, writeStdin closes the pipe so `opencode run`
// (which only starts executing after stdin EOF) actually starts. An open
// pipe hangs the turn before the first tool call.
func TestStdinPipe_PromptThenEOF(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pr, pw := io.Pipe()
	defer func() { _ = pr.Close() }()

	// In production launch() calls writeStdin in a goroutine; the consumer
	// (opencode) reads concurrently. Mirror that here.
	go writeStdin(pw, "prompt text", ctx)

	buf := make([]byte, 64)
	n, err := pr.Read(buf)
	if err != nil || string(buf[:n]) != "prompt text" {
		t.Fatalf("first read = %q, err %v; want %q", string(buf[:n]), err, "prompt text")
	}

	// The pipe must be closed (EOF) once the write completes.
	if _, err := pr.Read(buf); err != io.EOF {
		t.Fatalf("expected io.EOF after prompt write, got %v", err)
	}
}
