package grok

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const realStreamingMessagesFixture = `
{"type":"system","subtype":"init","session_id":"sid-fixture","model":"grok-4.5"}
{"type":"stream_event","event":{"type":"message_start","message":{"role":"assistant","content":[]}}}
{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}}
{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Need "}}}
{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"tool"}}}
{"type":"stream_event","event":{"type":"content_block_stop","index":0}}
{"type":"stream_event","event":{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tool-1","name":"Shell","input":{}}}}
{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"printf hi\"}"}}}
{"type":"stream_event","event":{"type":"content_block_stop","index":1}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"Need tool"},{"type":"tool_use","id":"tool-1","name":"Shell","input":{"command":"printf hi"}}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":"{\"output_for_prompt\":\"hi\\n\",\"exit_code\":0,\"output\":[104,105,10]}","is_error":false}]}}
{"type":"stream_event","event":{"type":"message_start","message":{"role":"assistant","content":[]}}}
{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}}
{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"DONE"}}}
{"type":"stream_event","event":{"type":"content_block_stop","index":0}}
{"type":"stream_event","event":{"type":"message_stop"}}
{"type":"assistant","message":{"role":"assistant","model":"grok-4.5","content":[{"type":"text","text":"DONE"}],"usage":{"input_tokens":80,"output_tokens":3,"cache_read_input_tokens":20,"cache_creation_input_tokens":1}}}
{"type":"result","subtype":"success","is_error":false,"result":"DONE","session_id":"sid-fixture","num_turns":2,"usage":{"input_tokens":100,"output_tokens":5,"cache_read_input_tokens":40,"cache_creation_input_tokens":2},"modelUsage":{"grok-4.5":{"contextWindow":262144}}}
`

func TestNewGrokSessionLifecycle(t *testing.T) {
	session, err := newGrokSession(context.Background(), sessionConfig{
		cmd:      "grok",
		workDir:  t.TempDir(),
		mode:     "yolo",
		resumeID: "resume-abc",
	})
	require.NoError(t, err)
	assert.True(t, session.Alive())
	assert.Equal(t, "resume-abc", session.CurrentSessionID())

	require.NoError(t, session.Close())
	require.NoError(t, session.Close(), "Close must be idempotent")
	assert.False(t, session.Alive())
	assert.Error(t, session.Send("after close", "", nil, nil))
}

func TestNewGrokSessionTreatsContinueAsFresh(t *testing.T) {
	session, err := newGrokSession(context.Background(), sessionConfig{
		cmd:      "grok",
		workDir:  t.TempDir(),
		mode:     "yolo",
		resumeID: core.ContinueSession,
	})
	require.NoError(t, err)
	defer func() { _ = session.Close() }()
	assert.Empty(t, session.CurrentSessionID())
}

func TestBuildArgsInheritLocalDefaults(t *testing.T) {
	session, err := newGrokSession(context.Background(), sessionConfig{
		cmd:     "grok",
		workDir: "/project",
		mode:    "yolo",
	})
	require.NoError(t, err)
	defer func() { _ = session.Close() }()

	args := session.buildArgs("/secure/prompt.txt")
	assertFlagValue(t, args, "--cwd", "/project")
	assertFlagValue(t, args, "--output-format", "streaming-messages-json")
	assertFlagValue(t, args, "--permission-mode", "bypassPermissions")
	assertFlagValue(t, args, "--prompt-file", "/secure/prompt.txt")
	assert.Contains(t, args, "--include-partial-messages")
	assert.Contains(t, args, "--no-ask-user")
	assert.Contains(t, args, "--no-auto-update")
	assert.Contains(t, args, "--always-approve")
	assert.NotContains(t, args, "--model", "empty model must inherit the direct local Grok default")
	assert.NotContains(t, args, "--resume")
	assert.NotContains(t, args, "--session-id", "--session-id starts a new session; it must never be used to resume")
}

func TestBuildArgsResumeAndExplicitOptions(t *testing.T) {
	session, err := newGrokSession(context.Background(), sessionConfig{
		cmd:             "grok",
		extraArgs:       []string{"wrapper-subcommand"},
		workDir:         "/project",
		model:           "grok-explicit",
		mode:            "plan",
		resumeID:        "sid-resume",
		reasoningEffort: "high",
		maxTurns:        4,
	})
	require.NoError(t, err)
	defer func() { _ = session.Close() }()

	args := session.buildArgs("/secure/prompt.txt")
	require.NotEmpty(t, args)
	assert.Equal(t, "wrapper-subcommand", args[0])
	assertFlagValue(t, args, "--resume", "sid-resume")
	assertFlagValue(t, args, "--model", "grok-explicit")
	assertFlagValue(t, args, "--reasoning-effort", "high")
	assertFlagValue(t, args, "--max-turns", "4")
	assertFlagValue(t, args, "--permission-mode", "plan")
	assert.NotContains(t, args, "--always-approve", "only yolo may force approval")
	assert.NotContains(t, args, "--session-id")
}

func TestBuildArgsAlwaysApproveOnlyForYolo(t *testing.T) {
	tests := []struct {
		mode          string
		alwaysApprove bool
	}{
		{mode: "yolo", alwaysApprove: true},
		{mode: "default"},
		{mode: "accept_edits"},
		{mode: "auto"},
		{mode: "dont_ask"},
		{mode: "plan"},
	}
	for _, test := range tests {
		t.Run(test.mode, func(t *testing.T) {
			session, err := newGrokSession(context.Background(), sessionConfig{
				cmd:     "grok",
				workDir: t.TempDir(),
				mode:    test.mode,
			})
			require.NoError(t, err)
			defer func() { _ = session.Close() }()
			assert.Equal(t, test.alwaysApprove, containsArg(session.buildArgs("prompt"), "--always-approve"))
		})
	}
}

func TestStreamingMessagesFixture(t *testing.T) {
	session, err := newGrokSession(context.Background(), sessionConfig{
		cmd:     "grok",
		workDir: t.TempDir(),
		mode:    "yolo",
	})
	require.NoError(t, err)
	defer func() { _ = session.Close() }()

	state := newStreamState()
	terminal := false
	for _, line := range strings.Split(strings.TrimSpace(realStreamingMessagesFixture), "\n") {
		var raw map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &raw))
		terminal = session.handleEvent(state, raw)
	}
	assert.True(t, terminal)

	events := readEvents(t, session.Events(), 6)
	require.Len(t, events, 6)
	assert.Equal(t, core.EventText, events[0].Type)
	assert.Empty(t, events[0].Content)
	assert.Equal(t, "sid-fixture", events[0].SessionID)

	assert.Equal(t, core.EventThinking, events[1].Type)
	assert.Equal(t, "Need tool", events[1].Content)

	assert.Equal(t, core.EventToolUse, events[2].Type)
	assert.Equal(t, "Shell", events[2].ToolName)
	assert.Equal(t, `{"command":"printf hi"}`, events[2].ToolInput)
	assert.Equal(t, "tool-1", events[2].RequestID)

	assert.Equal(t, core.EventToolResult, events[3].Type)
	assert.Equal(t, "Shell", events[3].ToolName)
	assert.Equal(t, "hi\n", events[3].ToolResult)
	require.NotNil(t, events[3].ToolExitCode)
	assert.Zero(t, *events[3].ToolExitCode)
	require.NotNil(t, events[3].ToolSuccess)
	assert.True(t, *events[3].ToolSuccess)
	assert.Equal(t, "completed", events[3].ToolStatus)

	assert.Equal(t, core.EventText, events[4].Type)
	assert.Equal(t, "DONE", events[4].Content)

	result := events[5]
	assert.Equal(t, core.EventResult, result.Type)
	assert.Equal(t, "DONE", result.Content)
	assert.Equal(t, "sid-fixture", result.SessionID)
	assert.True(t, result.Done)
	assert.Equal(t, 100, result.InputTokens)
	assert.Equal(t, 5, result.OutputTokens)
	assert.Equal(t, 40, result.CacheReadInputTokens)
	assert.Equal(t, 2, result.CacheCreationInputTokens)
	assert.Equal(t, float64(2), result.Metadata["num_turns"])
	assert.Zero(t, len(session.events), "complete assistant frames and result must not duplicate partial output")

	usage := session.GetContextUsage()
	require.NotNil(t, usage)
	assert.Equal(t, 101, usage.UsedTokens)
	assert.Equal(t, 106, usage.TotalTokens)
	assert.Equal(t, 262144, usage.ContextWindow)
}

func TestAssistantFallbackEmitsOnlyMissingSuffix(t *testing.T) {
	session, err := newGrokSession(context.Background(), sessionConfig{cmd: "grok", workDir: t.TempDir(), mode: "yolo"})
	require.NoError(t, err)
	defer func() { _ = session.Close() }()
	state := newStreamState()

	state.handlePartial(session, map[string]any{"type": "message_start"})
	state.handlePartial(session, map[string]any{
		"type":  "content_block_start",
		"index": float64(0),
		"content_block": map[string]any{
			"type": "text",
			"text": "",
		},
	})
	state.handlePartial(session, map[string]any{
		"type":  "content_block_delta",
		"index": float64(0),
		"delta": map[string]any{
			"type": "text_delta",
			"text": "DO",
		},
	})
	state.handlePartial(session, map[string]any{"type": "content_block_stop", "index": float64(0)})
	state.handleAssistantFallback(session, map[string]any{
		"message": map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "DONE"}},
		},
	})

	events := readEvents(t, session.Events(), 2)
	assert.Equal(t, "DO", events[0].Content)
	assert.Equal(t, "NE", events[1].Content)
	assert.Zero(t, len(session.events))
}

func TestSendAcceptsRealisticallyLargeNDJSONLine(t *testing.T) {
	workDir := t.TempDir()
	session := newHelperSession(t, workDir, "large-success")
	defer func() { _ = session.Close() }()

	require.NoError(t, session.Send("large", "message-large", nil, nil))
	events := collectUntilTerminal(t, session.Events())
	require.NoError(t, session.Close())

	var largeText string
	for _, event := range events {
		if event.Type == core.EventText && len(event.Content) > len(largeText) {
			largeText = event.Content
		}
	}
	assert.Len(t, largeText, 320*1024, "Scanner must accept Grok frames much larger than bufio.Scanner's 64 KiB default")
	assert.Equal(t, core.EventResult, events[len(events)-1].Type)
	assert.Equal(t, "large-complete", events[len(events)-1].Content)
}

func TestSendUsesSecurePromptFileAndPreservesAttachments(t *testing.T) {
	workDir := t.TempDir()
	reportPath := filepath.Join(t.TempDir(), "helper-report.json")
	session := newHelperSession(t, workDir, "capture-success", "GROK_HELPER_REPORT="+reportPath)

	images := []core.ImageAttachment{{MimeType: "image/jpeg", Data: []byte("jpeg-bytes")}}
	files := []core.FileAttachment{{MimeType: "text/plain", FileName: "../../notes.txt", Data: []byte("note-body")}}
	require.NoError(t, session.Send("inspect attachments", "../message-1", images, files))
	events := collectUntilTerminal(t, session.Events())
	require.Equal(t, core.EventResult, events[len(events)-1].Type)
	require.NoError(t, session.Close())

	data, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	var report grokHelperReport
	require.NoError(t, json.Unmarshal(data, &report))
	assert.Equal(t, uint32(0o600), report.PromptMode)
	assert.Contains(t, report.Prompt, "inspect attachments")
	assertFlagValue(t, report.Args, "--prompt-file", report.PromptPath)
	_, err = os.Stat(report.PromptPath)
	assert.ErrorIs(t, err, os.ErrNotExist, "temporary prompt must be removed after the turn")

	attachmentDir := filepath.Join(workDir, ".cc-connect", "attachments", "message-1")
	imagePath := filepath.Join(attachmentDir, "image_1.jpg")
	filePath := filepath.Join(attachmentDir, "notes.txt")
	assert.Contains(t, report.Prompt, imagePath)
	assert.Contains(t, report.Prompt, filePath)
	imageData, err := os.ReadFile(imagePath)
	require.NoError(t, err)
	assert.Equal(t, []byte("jpeg-bytes"), imageData)
	fileData, err := os.ReadFile(filePath)
	require.NoError(t, err)
	assert.Equal(t, []byte("note-body"), fileData)
	assert.NotContains(t, report.Prompt, "../", "untrusted attachment names must not escape the workspace")
}

func TestSendRedactsSecretsFromCLIError(t *testing.T) {
	const secret = "secret-marker-for-grok-test"
	session := newHelperSession(t, t.TempDir(), "fail", "XAI_API_KEY="+secret)
	defer func() { _ = session.Close() }()

	require.NoError(t, session.Send("fail", "", nil, nil))
	events := collectUntilTerminal(t, session.Events())
	require.Len(t, events, 1)
	require.Equal(t, core.EventError, events[0].Type)
	require.Error(t, events[0].Error)
	message := events[0].Error.Error()
	assert.NotContains(t, message, secret)
	assert.NotContains(t, message, "\x1b[")
	assert.Contains(t, message, "[REDACTED]")
}

func TestCancelTurnStopsProcessButKeepsSessionAlive(t *testing.T) {
	session := newHelperSession(t, t.TempDir(), "sleep")
	defer func() { _ = session.Close() }()
	require.NoError(t, session.Send("sleep", "", nil, nil))

	initEvent := readEvents(t, session.Events(), 1)[0]
	assert.Equal(t, "sid-sleep", initEvent.SessionID)
	require.NoError(t, session.CancelTurn())
	cancelEvent := readEvents(t, session.Events(), 1)[0]
	assert.Equal(t, core.EventError, cancelEvent.Type)
	require.Error(t, cancelEvent.Error)
	assert.Contains(t, cancelEvent.Error.Error(), "turn stopped")
	assert.True(t, session.Alive(), "CancelTurn must not destroy conversation continuity")
}

func TestCloseWhileTurnActiveIsConcurrentAndIdempotent(t *testing.T) {
	session := newHelperSession(t, t.TempDir(), "sleep")
	require.NoError(t, session.Send("sleep", "", nil, nil))
	_ = readEvents(t, session.Events(), 1)

	const closers = 8
	errorsCh := make(chan error, closers)
	var wg sync.WaitGroup
	for range closers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errorsCh <- session.Close()
		}()
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent Close calls deadlocked with an active turn")
	}
	close(errorsCh)
	for err := range errorsCh {
		require.NoError(t, err)
	}
	assert.False(t, session.Alive())
	waitForClosedEvents(t, session.Events())
}

func newHelperSession(t *testing.T, workDir, mode string, extraEnv ...string) *grokSession {
	t.Helper()
	env := []string{
		"GO_WANT_GROK_HELPER=1",
		"GROK_HELPER_MODE=" + mode,
	}
	env = append(env, extraEnv...)
	session, err := newGrokSession(context.Background(), sessionConfig{
		cmd:       os.Args[0],
		extraArgs: []string{"-test.run=^TestGrokHelperProcess$", "--"},
		workDir:   workDir,
		mode:      "yolo",
		extraEnv:  env,
	})
	require.NoError(t, err)
	return session
}

func TestGrokHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_GROK_HELPER") != "1" {
		return
	}

	switch os.Getenv("GROK_HELPER_MODE") {
	case "capture-success":
		promptPath := helperArgValue(os.Args[1:], "--prompt-file")
		prompt, err := os.ReadFile(promptPath)
		helperMust(err)
		info, err := os.Stat(promptPath)
		helperMust(err)
		report := grokHelperReport{
			Args:       append([]string(nil), os.Args[1:]...),
			PromptPath: promptPath,
			Prompt:     string(prompt),
			PromptMode: uint32(info.Mode().Perm()),
		}
		data, err := json.Marshal(report)
		helperMust(err)
		helperMust(os.WriteFile(os.Getenv("GROK_HELPER_REPORT"), data, 0o600))
		helperWriteJSON(map[string]any{"type": "system", "session_id": "sid-capture"})
		helperWriteJSON(map[string]any{"type": "result", "subtype": "success", "result": "captured", "session_id": "sid-capture"})
		os.Exit(0)
	case "large-success":
		large := strings.Repeat("x", 320*1024)
		helperWriteJSON(map[string]any{"type": "system", "session_id": "sid-large"})
		helperWriteJSON(map[string]any{"type": "stream_event", "event": map[string]any{"type": "message_start"}})
		helperWriteJSON(map[string]any{"type": "stream_event", "event": map[string]any{
			"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "text", "text": ""},
		}})
		helperWriteJSON(map[string]any{"type": "stream_event", "event": map[string]any{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": large},
		}})
		helperWriteJSON(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_stop", "index": 0}})
		helperWriteJSON(map[string]any{"type": "assistant", "message": map[string]any{
			"content": []any{map[string]any{"type": "text", "text": large}},
		}})
		helperWriteJSON(map[string]any{"type": "result", "subtype": "success", "result": "large-complete", "session_id": "sid-large"})
		os.Exit(0)
	case "fail":
		_, _ = fmt.Fprintf(os.Stderr, "\x1b[31mfailed using %s\x1b[0m\n", os.Getenv("XAI_API_KEY"))
		os.Exit(9)
	case "sleep":
		helperWriteJSON(map[string]any{"type": "system", "session_id": "sid-sleep"})
		time.Sleep(30 * time.Second)
		os.Exit(0)
	default:
		_, _ = fmt.Fprintf(os.Stderr, "unknown helper mode %q\n", os.Getenv("GROK_HELPER_MODE"))
		os.Exit(2)
	}
}

type grokHelperReport struct {
	Args       []string `json:"args"`
	PromptPath string   `json:"prompt_path"`
	Prompt     string   `json:"prompt"`
	PromptMode uint32   `json:"prompt_mode"`
}

func helperWriteJSON(value any) {
	helperMust(json.NewEncoder(os.Stdout).Encode(value))
}

func helperMust(err error) {
	if err != nil {
		_, _ = io.WriteString(os.Stderr, err.Error()+"\n")
		os.Exit(3)
	}
}

func helperArgValue(args []string, flag string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

func assertFlagValue(t *testing.T, args []string, flag, want string) {
	t.Helper()
	for i := 0; i < len(args); i++ {
		if args[i] != flag {
			continue
		}
		require.Less(t, i+1, len(args), "%s has no value in %v", flag, args)
		assert.Equal(t, want, args[i+1], "args=%v", args)
		return
	}
	t.Fatalf("missing %s in args %v", flag, args)
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func readEvents(t *testing.T, events <-chan core.Event, count int) []core.Event {
	t.Helper()
	result := make([]core.Event, 0, count)
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for len(result) < count {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatalf("event channel closed after %d of %d events", len(result), count)
			}
			result = append(result, event)
		case <-timer.C:
			t.Fatalf("timed out after %d of %d events", len(result), count)
		}
	}
	return result
}

func collectUntilTerminal(t *testing.T, events <-chan core.Event) []core.Event {
	t.Helper()
	var result []core.Event
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatalf("event channel closed before a terminal event: %+v", result)
			}
			result = append(result, event)
			if event.Type == core.EventResult || event.Type == core.EventError {
				return result
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for terminal event: %+v", result)
		}
	}
}

func waitForClosedEvents(t *testing.T, events <-chan core.Event) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case _, ok := <-events:
			if !ok {
				return
			}
		case <-timer.C:
			t.Fatal("event channel was not closed")
		}
	}
}
