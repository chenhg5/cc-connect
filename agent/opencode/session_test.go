package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
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
		"--thinking",
		"--file", "/tmp/a.png",
		"--file", "/tmp/b.jpg",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

// opencode v2's `run` rejects --dir, so the work dir must only reach opencode
// as the process cwd (#2076).
func TestOpencodeSessionBuildRunArgsOmitsDirFlag(t *testing.T) {
	s := &opencodeSession{workDir: "/repo"}

	for _, arg := range s.buildRunArgs("hi", nil, "") {
		if arg == "--dir" || strings.HasPrefix(arg, "--dir=") {
			t.Fatalf("buildRunArgs passed %q to opencode run", arg)
		}
	}
}

// TestOpencodeSessionSendRunsInWorkDir verifies that the opencode process is
// actually started in the configured work_dir now that `--dir` is no longer
// passed on the command line (#2076). Without this check, dropping the flag
// could silently drop the work dir as well.
//
// The test binary doubles as a fake opencode: when GO_WANT_OPENCODE_HELPER=1
// is set it records its own working directory and exits.
func TestOpencodeSessionSendRunsInWorkDir(t *testing.T) {
	if os.Getenv("GO_WANT_OPENCODE_HELPER") == "1" {
		cwdFile := os.Getenv("CC_OPENCODE_CWD_FILE")
		if cwdFile == "" {
			os.Exit(2)
		}
		wd, err := os.Getwd()
		if err != nil {
			os.Exit(2)
		}
		if err := os.WriteFile(cwdFile, []byte(wd), 0o600); err != nil {
			os.Exit(2)
		}
		_, _ = os.Stdout.WriteString(`{"type":"text","part":{"text":"cwd recorded"}}` + "\n")
		os.Exit(0)
	}

	cwdFile := filepath.Join(t.TempDir(), "cwd.txt")
	workDir := t.TempDir()

	s, err := newOpencodeSession(
		context.Background(),
		os.Args[0],
		[]string{"-test.run=TestOpencodeSessionSendRunsInWorkDir", "--"},
		workDir,
		"",
		"default",
		"",
		"",
		[]string{
			"GO_WANT_OPENCODE_HELPER=1",
			"CC_OPENCODE_CWD_FILE=" + cwdFile,
		},
	)
	if err != nil {
		t.Fatalf("newOpencodeSession: %v", err)
	}
	defer func() { _ = s.Close() }()

	if err := s.Send("hello", "", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	timeout := time.After(30 * time.Second)
wait:
	for {
		select {
		case evt := <-s.Events():
			if evt.Type == core.EventResult {
				break wait
			}
		case <-timeout:
			t.Fatal("timeout waiting for the fake opencode process to finish")
		}
	}

	raw, err := os.ReadFile(cwdFile)
	if err != nil {
		t.Fatalf("read recorded cwd: %v", err)
	}
	got, err := filepath.EvalSymlinks(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("resolve recorded cwd: %v", err)
	}
	want, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		t.Fatalf("resolve work dir: %v", err)
	}
	if got != want {
		t.Fatalf("opencode process cwd = %q, want work_dir %q", got, want)
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

// verify Agent implements core.Agent
var _ core.Agent = (*Agent)(nil)

func decodeSessionPermissionEnv(t *testing.T, session core.AgentSession) map[string]any {
	t.Helper()
	oc, ok := session.(*opencodeSession)
	if !ok {
		t.Fatalf("session type = %T, want *opencodeSession", session)
	}
	var permissions map[string]any
	if err := json.Unmarshal([]byte(envValue(oc.extraEnv, "OPENCODE_PERMISSION")), &permissions); err != nil {
		t.Fatalf("OPENCODE_PERMISSION is invalid JSON: %v", err)
	}
	return permissions
}

func TestAgentStartSessionDeniesHeadlessQuestionTool(t *testing.T) {
	t.Setenv("OPENCODE_PERMISSION", `{"bash":"ask","question":"allow"}`)
	a := &Agent{
		workDir:   t.TempDir(),
		model:     "openai/test",
		mode:      "default",
		cmd:       "true",
		activeIdx: -1,
	}
	session, err := a.StartSession(context.Background(), "ses_test")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	permissions := decodeSessionPermissionEnv(t, session)
	if got := permissions["question"]; got != "deny" {
		t.Fatalf("question permission = %#v, want deny", got)
	}
	if got := permissions["bash"]; got != "ask" {
		t.Fatalf("bash permission = %#v, want preserved ask", got)
	}
}

func TestAgentStartSessionMergesSessionPermissionOverride(t *testing.T) {
	t.Setenv("OPENCODE_PERMISSION", `{"bash":"allow"}`)
	a := &Agent{
		workDir:   t.TempDir(),
		model:     "openai/test",
		mode:      "default",
		cmd:       "true",
		activeIdx: -1,
		sessionEnv: []string{
			`OPENCODE_PERMISSION={"edit":"ask","question":"allow"}`,
		},
	}
	session, err := a.StartSession(context.Background(), "ses_test")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	permissions := decodeSessionPermissionEnv(t, session)
	if got := permissions["question"]; got != "deny" {
		t.Fatalf("question permission = %#v, want deny", got)
	}
	if got := permissions["edit"]; got != "ask" {
		t.Fatalf("edit permission = %#v, want session override preserved", got)
	}
	if _, ok := permissions["bash"]; ok {
		t.Fatalf("process permission should be replaced by session override: %#v", permissions)
	}
}

func TestAgentStartSessionHandlesUnusablePermissionEnv(t *testing.T) {
	for _, raw := range []string{"null", "not json", `["question"]`} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("OPENCODE_PERMISSION", raw)
			a := &Agent{workDir: t.TempDir(), model: "openai/test", cmd: "true", activeIdx: -1}
			session, err := a.StartSession(context.Background(), "")
			if err != nil {
				t.Fatalf("StartSession: %v", err)
			}
			defer func() { _ = session.Close() }()
			oc := session.(*opencodeSession)
			if got := envValue(oc.extraEnv, "OPENCODE_PERMISSION"); got != `{"question":"deny"}` {
				t.Fatalf("OPENCODE_PERMISSION = %q, want question deny", got)
			}
		})
	}
}

func stubOpenCodeServer(t *testing.T, start func(extraArgs []string) (string, error)) {
	t.Helper()
	original := startOpenCodeServer
	startOpenCodeServer = func(_ context.Context, _ string, extraArgs []string, _ string, _ []string) (string, func(), error) {
		baseURL, err := start(extraArgs)
		if err != nil {
			return "", nil, err
		}
		return baseURL, func() {}, nil
	}
	t.Cleanup(func() { startOpenCodeServer = original })
}

func TestOpencodeSessionDeniesQuestionOnResumedSession(t *testing.T) {
	workDir := t.TempDir()
	type permissionRule struct {
		Permission string `json:"permission"`
		Pattern    string `json:"pattern"`
		Action     string `json:"action"`
	}
	updated := make(chan []permissionRule, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session/ses_test" {
			t.Errorf("path = %q, want session path", r.URL.Path)
		}
		if got := r.URL.Query().Get("directory"); got != workDir {
			t.Errorf("directory = %q, want %q", got, workDir)
		}
		username, password, ok := r.BasicAuth()
		if !ok || username != "tester" || password != "secret" {
			t.Errorf("basic auth = (%q, %q, %v), want tester credentials", username, password, ok)
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"permission":[{"permission":"bash","pattern":"*","action":"allow"},{"permission":"question","pattern":"*","action":"allow"},{"permission":"question","pattern":"custom","action":"ask"}]}`))
		case http.MethodPatch:
			var body struct {
				Permission []permissionRule `json:"permission"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode update: %v", err)
			}
			updated <- body.Permission
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"ses_test"}`))
		default:
			t.Errorf("method = %s, want GET or PATCH", r.Method)
		}
	}))
	defer server.Close()

	var gotExtraArgs []string
	stubOpenCodeServer(t, func(extraArgs []string) (string, error) {
		gotExtraArgs = extraArgs
		return server.URL, nil
	})

	wrapperArgs := []string{"--wrapper-flag"}
	s, err := newOpencodeSession(context.Background(), "opencode", wrapperArgs, workDir, "openai/test", "default", "", "ses_test", []string{
		"OPENCODE_SERVER_USERNAME=tester",
		"OPENCODE_SERVER_PASSWORD=secret",
	})
	if err != nil {
		t.Fatalf("newOpencodeSession: %v", err)
	}
	defer func() { _ = s.Close() }()
	if err := s.denyHeadlessSessionQuestion("ses_test"); err != nil {
		t.Fatalf("denyHeadlessSessionQuestion: %v", err)
	}
	if !reflect.DeepEqual(gotExtraArgs, wrapperArgs) {
		t.Fatalf("server extraArgs = %v, want %v", gotExtraArgs, wrapperArgs)
	}

	select {
	case permissions := <-updated:
		if len(permissions) != 1 {
			t.Fatalf("permissions = %+v, want one appended question deny", permissions)
		}
		if permissions[0] != (permissionRule{Permission: "question", Pattern: "*", Action: "deny"}) {
			t.Fatalf("question permission = %+v, want wildcard deny", permissions[0])
		}
	case <-time.After(time.Second):
		t.Fatal("session permission update not received")
	}
}

func TestOpencodeSessionKeepsExistingFinalQuestionDeny(t *testing.T) {
	var patches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			patches.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"permission":[{"permission":"question","pattern":"*","action":"allow"},{"permission":"question","pattern":"*","action":"deny"}]}`))
	}))
	defer server.Close()

	stubOpenCodeServer(t, func([]string) (string, error) { return server.URL, nil })

	s, err := newOpencodeSession(context.Background(), "opencode", nil, t.TempDir(), "openai/test", "default", "", "ses_test", nil)
	if err != nil {
		t.Fatalf("newOpencodeSession: %v", err)
	}
	defer func() { _ = s.Close() }()
	if err := s.denyHeadlessSessionQuestion("ses_test"); err != nil {
		t.Fatalf("denyHeadlessSessionQuestion: %v", err)
	}
	if got := patches.Load(); got != 0 {
		t.Fatalf("PATCH requests = %d, want 0 for an already denied question tool", got)
	}
}

// captureWarnLogs redirects the default slog logger into a buffer for the test.
func captureWarnLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// newResumedTestSession returns a resumed session whose temporary server is
// replaced by start, plus a counter of how often that server was started.
func newResumedTestSession(t *testing.T, start func(call int) (string, error)) (*opencodeSession, *atomic.Int32) {
	t.Helper()
	var starts atomic.Int32
	stubOpenCodeServer(t, func([]string) (string, error) {
		return start(int(starts.Add(1)))
	})
	s, err := newOpencodeSession(context.Background(), "opencode", nil, t.TempDir(), "openai/test", "default", "", "ses_test", nil)
	if err != nil {
		t.Fatalf("newOpencodeSession: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, &starts
}

// A preparation failure must not fail the message, and the expensive
// preparation must not be repeated on every later message.
func TestOpencodeSessionSendSurvivesPreparationFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as the fake opencode binary")
	}
	logs := captureWarnLogs(t)

	dir := t.TempDir()
	record := filepath.Join(dir, "runs.txt")
	script := filepath.Join(dir, "fake-opencode")
	body := "#!/bin/sh\n{ printf '%s ' \"$@\"; cat; echo; } >> \"$FAKE_OPENCODE_RECORD\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake opencode: %v", err)
	}

	var starts atomic.Int32
	stubOpenCodeServer(t, func([]string) (string, error) {
		starts.Add(1)
		return "", fmt.Errorf("serve is broken")
	})

	s, err := newOpencodeSession(context.Background(), script, nil, dir, "openai/test", "default", "", "ses_test",
		[]string{"FAKE_OPENCODE_RECORD=" + record})
	if err != nil {
		t.Fatalf("newOpencodeSession: %v", err)
	}
	defer func() { _ = s.Close() }()

	prompts := []string{"first-message", "second-message", "third-message", "fourth-message"}
	for i, prompt := range prompts {
		if err := s.Send(prompt, "", nil, nil); err != nil {
			t.Fatalf("Send %d failed although preparation is best effort: %v", i+1, err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			data, _ := os.ReadFile(record)
			if strings.Contains(string(data), prompt+"\n") {
				if !strings.Contains(string(data), "--session ses_test") {
					t.Fatalf("turn did not resume the session: %q", data)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("prompt %q was never delivered to the opencode process; record: %q", prompt, data)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	if got := starts.Load(); got != maxHeadlessPrepareAttempts {
		t.Fatalf("server starts = %d over %d sends, want %d (one retry, then give up)", got, len(prompts), maxHeadlessPrepareAttempts)
	}
	out := logs.String()
	if !strings.Contains(out, "did not start") {
		t.Fatalf("log does not say the server did not start: %s", out)
	}
	if strings.Contains(out, "could not be applied") || strings.Contains(out, "does not support") {
		t.Fatalf("server start failure logged as a different failure: %s", out)
	}
}

func TestOpencodeSessionRetriesPermissionPreparationOnce(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"permission":[{"permission":"question","pattern":"*","action":"deny"}]}`))
	}))
	defer server.Close()

	s, starts := newResumedTestSession(t, func(call int) (string, error) {
		if call == 1 {
			return "", fmt.Errorf("temporary start failure")
		}
		return server.URL, nil
	})

	s.prepareHeadlessSession("ses_test", true)
	if s.prepared {
		t.Fatal("session settled after one failed attempt; want one retry left")
	}
	s.prepareHeadlessSession("ses_test", true)
	if !s.prepared || starts.Load() != 2 {
		t.Fatalf("prepared = %v, server starts = %d; want true and 2", s.prepared, starts.Load())
	}
	s.prepareHeadlessSession("ses_test", true)
	if starts.Load() != 2 {
		t.Fatalf("server starts = %d after a successful preparation, want 2", starts.Load())
	}
}

func TestOpencodeSessionPermissionShapes(t *testing.T) {
	const denyRuleUpdate = `{"permission":[{"action":"deny","pattern":"*","permission":"question"}]}`
	tests := []struct {
		name      string
		session   string // GET response body
		wantPatch string // expected PATCH body; "" means no PATCH
	}{
		{"missing field", `{"id":"ses_test"}`, denyRuleUpdate},
		{"null field", `{"id":"ses_test","permission":null}`, denyRuleUpdate},
		{"empty array", `{"permission":[]}`, denyRuleUpdate},
		{"array allowing question", `{"permission":[{"permission":"question","pattern":"*","action":"allow"}]}`, denyRuleUpdate},
		{"array already denying", `{"permission":[{"permission":"bash","pattern":"*","action":"allow"},{"permission":"*","pattern":"*","action":"deny"}]}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureWarnLogs(t)
			var patches []string
			var mu sync.Mutex
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodPatch {
					body, _ := io.ReadAll(r.Body)
					mu.Lock()
					patches = append(patches, string(body))
					mu.Unlock()
					// Echo the stored rules back like the real server does.
					_, _ = w.Write(body)
					return
				}
				_, _ = w.Write([]byte(tt.session))
			}))
			defer server.Close()

			s, starts := newResumedTestSession(t, func(int) (string, error) { return server.URL, nil })
			if err := s.denyHeadlessSessionQuestion("ses_test"); err != nil {
				t.Fatalf("denyHeadlessSessionQuestion: %v", err)
			}
			s.prepareHeadlessSession("ses_test", true)
			if !s.prepared {
				t.Fatal("session not settled after a successful preparation")
			}
			if starts.Load() != 2 {
				t.Fatalf("server starts = %d, want 2", starts.Load())
			}

			mu.Lock()
			defer mu.Unlock()
			if tt.wantPatch == "" {
				if len(patches) != 0 {
					t.Fatalf("unexpected PATCH for an already denied question tool: %v", patches)
				}
			} else {
				if len(patches) != 2 || patches[0] != tt.wantPatch || patches[1] != tt.wantPatch {
					t.Fatalf("PATCH bodies = %v, want twice %s", patches, tt.wantPatch)
				}
			}
			if out := logs.String(); out != "" {
				t.Fatalf("unexpected warning: %s", out)
			}
		})
	}
}

// Answers that mean "this OpenCode build cannot do this" (400/404/405/422, a
// non-session body, an unknown permission shape such as a map) are given up
// at once; answers that may be transient get exactly one retry.
func TestOpencodeSessionPreparationDegradesGracefully(t *testing.T) {
	const rules = `{"permission":[{"permission":"question","pattern":"*","action":"allow"}]}`
	tests := []struct {
		name        string
		getStatus   int
		getBody     string
		patchStatus int
		unsupported bool
		wantStarts  int32
		wantLog     string
	}{
		{"GET 404", http.StatusNotFound, `not found`, 0, true, 1, "does not support"},
		{"GET 400", http.StatusBadRequest, ``, 0, true, 1, "does not support"},
		{"GET not JSON", http.StatusOK, `<!doctype html><html></html>`, 0, true, 1, "does not support"},
		{"GET unknown permission shape", http.StatusOK, `{"permission":"deny"}`, 0, true, 1, "does not support"},
		{"GET map-shaped permission", http.StatusOK, `{"permission":{"question":"allow"}}`, 0, true, 1, "does not support"},
		{"GET malformed rules", http.StatusOK, `{"permission":["question"]}`, 0, true, 1, "does not support"},
		{"PATCH 404", http.StatusOK, rules, http.StatusNotFound, true, 1, "does not support"},
		{"PATCH 400", http.StatusOK, rules, http.StatusBadRequest, true, 1, "does not support"},
		{"PATCH 405", http.StatusOK, rules, http.StatusMethodNotAllowed, true, 1, "does not support"},
		{"PATCH 422", http.StatusOK, rules, http.StatusUnprocessableEntity, true, 1, "does not support"},
		{"GET 401", http.StatusUnauthorized, ``, 0, false, 2, "could not be applied"},
		{"GET 429", http.StatusTooManyRequests, ``, 0, false, 2, "could not be applied"},
		{"PATCH 403", http.StatusOK, rules, http.StatusForbidden, false, 2, "could not be applied"},
		{"PATCH 408", http.StatusOK, rules, http.StatusRequestTimeout, false, 2, "could not be applied"},
		{"GET 500", http.StatusInternalServerError, `boom`, 0, false, 2, "could not be applied"},
		{"PATCH 500", http.StatusOK, rules, http.StatusInternalServerError, false, 2, "could not be applied"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureWarnLogs(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPatch {
					w.WriteHeader(tt.patchStatus)
					return
				}
				w.WriteHeader(tt.getStatus)
				_, _ = w.Write([]byte(tt.getBody))
			}))
			defer server.Close()

			s, starts := newResumedTestSession(t, func(int) (string, error) { return server.URL, nil })
			for i := 0; i < 4; i++ {
				s.prepareHeadlessSession("ses_test", true)
			}
			if !s.prepared {
				t.Fatal("session not settled; every later message would repeat the preparation")
			}
			if got := starts.Load(); got != tt.wantStarts {
				t.Fatalf("server starts = %d, want %d", got, tt.wantStarts)
			}
			out := logs.String()
			if !strings.Contains(out, tt.wantLog) {
				t.Fatalf("log misses %q: %s", tt.wantLog, out)
			}
			if strings.Contains(out, "did not start") {
				t.Fatalf("API failure logged as a server start failure: %s", out)
			}

			err := s.denyHeadlessSessionQuestion("ses_test")
			if err == nil {
				t.Fatal("denyHeadlessSessionQuestion unexpectedly succeeded")
			}
			if got := errors.Is(err, errQuestionDenyUnsupported); got != tt.unsupported {
				t.Fatalf("errors.Is(err, errQuestionDenyUnsupported) = %v, want %v (err: %v)", got, tt.unsupported, err)
			}
			if errors.Is(err, errOpenCodeServerStart) {
				t.Fatalf("API failure reported as a server start failure: %v", err)
			}
		})
	}
}

func TestOpencodeSessionSkipsPermissionPreparationForNewSession(t *testing.T) {
	stubOpenCodeServer(t, func([]string) (string, error) {
		t.Error("opencode server started for a brand-new session")
		return "", fmt.Errorf("unexpected")
	})

	s, err := newOpencodeSession(context.Background(), "opencode", nil, t.TempDir(), "openai/test", "default", "", "", nil)
	if err != nil {
		t.Fatalf("newOpencodeSession: %v", err)
	}
	defer func() { _ = s.Close() }()
	s.prepareHeadlessSession("", false)
	if !s.prepared {
		t.Fatal("new session not marked prepared")
	}
}
