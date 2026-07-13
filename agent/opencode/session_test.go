package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
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

func TestOpencodeHTTPMode_StreamsBeforePromptReturnsAndKeepsBackgroundEvents(t *testing.T) {
	events := make(chan string, 16)
	sseConnected := make(chan struct{})
	promptStarted := make(chan struct{})
	releasePrompt := make(chan struct{})
	permissionReply := make(chan map[string]any, 1)
	questionReply := make(chan map[string]any, 1)
	var connectedOnce sync.Once

	mux := http.NewServeMux()
	mux.HandleFunc("POST /session", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ses_http"}`))
	})
	mux.HandleFunc("GET /event", func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		connectedOnce.Do(func() { close(sseConnected) })
		for {
			select {
			case event := <-events:
				_, _ = fmt.Fprintf(w, "data: %s\n\n", event)
				flusher.Flush()
			case <-r.Context().Done():
				return
			}
		}
	})
	mux.HandleFunc("POST /session/ses_http/message", func(w http.ResponseWriter, _ *http.Request) {
		close(promptStarted)
		<-releasePrompt
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"info":{},"parts":[]}`))
	})
	mux.HandleFunc("POST /permission/per_1/reply", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		permissionReply <- body
		_, _ = w.Write([]byte(`true`))
	})
	mux.HandleFunc("POST /question/que_1/reply", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		questionReply <- body
		_, _ = w.Write([]byte(`true`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	agent, err := New(map[string]any{
		"cmd":            "/bin/false",
		"work_dir":       t.TempDir(),
		"connection_url": server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	select {
	case <-sseConnected:
	case <-time.After(time.Second):
		t.Fatal("SSE was not connected before Send")
	}

	sendDone := make(chan error, 1)
	go func() { sendDone <- session.Send("hello", nil, nil) }()
	select {
	case <-promptStarted:
	case <-time.After(time.Second):
		t.Fatal("prompt request did not start")
	}

	events <- `{"type":"message.part.updated","properties":{"part":{"id":"prt_user","messageID":"msg_user","type":"text","sessionID":"ses_http","text":"hello"}}}`
	assertNoOpencodeEvent(t, session.Events())

	events <- `{"type":"session.status","properties":{"sessionID":"ses_http","status":{"type":"busy"}}}`
	events <- `{"type":"message.part.updated","properties":{"part":{"id":"prt_step","messageID":"msg_assistant","type":"step-start","sessionID":"ses_http"}}}`
	events <- `{"type":"message.part.updated","properties":{"part":{"id":"prt_1","messageID":"msg_assistant","type":"text","sessionID":"ses_http","text":"fi"}}}`
	if event := waitOpencodeEvent(t, session.Events()); event.Type != core.EventText || event.Content != "fi" {
		t.Fatalf("first event = %#v, want streamed text", event)
	}
	events <- `{"type":"message.part.updated","properties":{"part":{"id":"prt_1","messageID":"msg_assistant","type":"text","sessionID":"ses_http","text":"first","time":{"end":1}}}}`
	if event := waitOpencodeEvent(t, session.Events()); event.Type != core.EventText || event.Content != "rst" {
		t.Fatalf("text delta = %#v, want streamed delta", event)
	}
	select {
	case err := <-sendDone:
		t.Fatalf("Send returned before prompt response: %v", err)
	default:
	}

	events <- `{"type":"session.status","properties":{"sessionID":"ses_http","status":{"type":"idle"}}}`
	if event := waitOpencodeEvent(t, session.Events()); event.Type != core.EventResult {
		t.Fatalf("idle event = %#v, want EventResult", event)
	}
	close(releasePrompt)
	if err := <-sendDone; err != nil {
		t.Fatal(err)
	}

	events <- `{"type":"session.status","properties":{"sessionID":"ses_http","status":{"type":"busy"}}}`
	events <- `{"type":"message.part.updated","properties":{"part":{"id":"prt_bg_step","messageID":"msg_background","type":"step-start","sessionID":"ses_http"}}}`
	events <- `{"type":"message.part.updated","properties":{"part":{"id":"prt_bg_text","messageID":"msg_background","type":"text","sessionID":"ses_http","text":"background done","time":{"end":1}}}}`
	events <- `{"type":"session.status","properties":{"sessionID":"ses_http","status":{"type":"idle"}}}`
	if event := waitOpencodeEvent(t, session.Events()); event.Type != core.EventText || event.Content != "background done" {
		t.Fatalf("background event = %#v, want unsolicited text", event)
	}
	if event := waitOpencodeEvent(t, session.Events()); event.Type != core.EventResult {
		t.Fatalf("background idle = %#v, want EventResult", event)
	}

	events <- `{"type":"permission.asked","properties":{"id":"per_1","sessionID":"ses_http","permission":"bash","patterns":["git status"],"metadata":{},"always":[]}}`
	permission := waitOpencodeEvent(t, session.Events())
	if permission.Type != core.EventPermissionRequest || permission.RequestID != "per_1" || permission.ToolName != "bash" {
		t.Fatalf("permission event = %#v", permission)
	}
	if err := session.RespondPermission("per_1", core.PermissionResult{Behavior: "allow"}); err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-permissionReply:
		if body["reply"] != "once" {
			t.Fatalf("permission reply = %#v, want once", body)
		}
	case <-time.After(time.Second):
		t.Fatal("permission reply was not posted")
	}

	events <- `{"type":"question.asked","properties":{"id":"que_1","sessionID":"ses_http","questions":[{"question":"Pick a path","header":"Decision","options":[{"label":"A","description":"Alpha"},{"label":"B","description":"Beta"}],"multiple":false}]}}`
	question := waitOpencodeEvent(t, session.Events())
	if question.Type != core.EventPermissionRequest || question.RequestID != "que_1" || question.ToolName != "AskUserQuestion" {
		t.Fatalf("question event = %#v", question)
	}
	if len(question.Questions) != 1 || question.Questions[0].Question != "Pick a path" || len(question.Questions[0].Options) != 2 {
		t.Fatalf("parsed questions = %#v", question.Questions)
	}
	if err := session.RespondPermission("que_1", core.PermissionResult{
		Behavior: "allow",
		UpdatedInput: map[string]any{
			"answers": map[string]any{"Pick a path": "A"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-questionReply:
		answers, ok := body["answers"].([]any)
		if !ok || len(answers) != 1 {
			t.Fatalf("question reply = %#v, want one answer row", body)
		}
		firstAnswer, ok := answers[0].([]any)
		if !ok || len(firstAnswer) != 1 || firstAnswer[0] != "A" {
			t.Fatalf("question answers = %#v, want [[A]]", body["answers"])
		}
	case <-time.After(time.Second):
		t.Fatal("question reply was not posted")
	}
}

func waitOpencodeEvent(t *testing.T, events <-chan core.Event) core.Event {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for OpenCode event")
		return core.Event{}
	}
}

func assertNoOpencodeEvent(t *testing.T, events <-chan core.Event) {
	t.Helper()
	select {
	case event := <-events:
		t.Fatalf("unexpected OpenCode event: %#v", event)
	case <-time.After(100 * time.Millisecond):
	}
}

// verify Agent implements core.Agent
var _ core.Agent = (*Agent)(nil)
