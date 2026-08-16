package grok

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
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

func TestAssistantCompleteEmitsOnlyMissingBlockSuffix(t *testing.T) {
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
	state.handleAssistantComplete(session, map[string]any{
		"message": map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "DONE"}},
		},
	})

	events := readEvents(t, session.Events(), 2)
	assert.Equal(t, "DO", events[0].Content)
	assert.Equal(t, "NE", events[1].Content)
	assert.Zero(t, len(session.events))
}

// TestMultiTextBlockAssistantKeepsEachStreamedBlock covers the Grok 4.6 shape
// where one assistant message has multiple text blocks. The old global
// streamText reconcile compared each complete block against the concatenation
// of all text deltas and falsely dropped both.
func TestMultiTextBlockAssistantKeepsEachStreamedBlock(t *testing.T) {
	session, err := newGrokSession(context.Background(), sessionConfig{cmd: "grok", workDir: t.TempDir(), mode: "yolo"})
	require.NoError(t, err)
	defer func() { _ = session.Close() }()
	state := newStreamState()

	state.handlePartial(session, map[string]any{"type": "message_start"})
	// Block 0: short narration (4.6 text-before-tool style).
	state.handlePartial(session, map[string]any{
		"type":          "content_block_start",
		"index":         float64(0),
		"content_block": map[string]any{"type": "text", "text": ""},
	})
	state.handlePartial(session, map[string]any{
		"type":  "content_block_delta",
		"index": float64(0),
		"delta": map[string]any{"type": "text_delta", "text": "先做两步。"},
	})
	state.handlePartial(session, map[string]any{"type": "content_block_stop", "index": float64(0)})
	// Block 1: tool
	state.handlePartial(session, map[string]any{
		"type":  "content_block_start",
		"index": float64(1),
		"content_block": map[string]any{
			"type":  "tool_use",
			"id":    "tool-a",
			"name":  "Shell",
			"input": map[string]any{},
		},
	})
	state.handlePartial(session, map[string]any{
		"type":  "content_block_delta",
		"index": float64(1),
		"delta": map[string]any{"type": "input_json_delta", "partial_json": `{"command":"true"}`},
	})
	state.handlePartial(session, map[string]any{"type": "content_block_stop", "index": float64(1)})
	// Block 2: second text in the same message.
	state.handlePartial(session, map[string]any{
		"type":          "content_block_start",
		"index":         float64(2),
		"content_block": map[string]any{"type": "text", "text": ""},
	})
	state.handlePartial(session, map[string]any{
		"type":  "content_block_delta",
		"index": float64(2),
		"delta": map[string]any{"type": "text_delta", "text": "然后继续。"},
	})
	state.handlePartial(session, map[string]any{"type": "content_block_stop", "index": float64(2)})

	// Complete frame with the same multi-text layout. Must not re-emit or drop.
	state.handleAssistantComplete(session, map[string]any{
		"message": map[string]any{
			"model": "grok-4.6",
			"content": []any{
				map[string]any{"type": "text", "text": "先做两步。"},
				map[string]any{"type": "tool_use", "id": "tool-a", "name": "Shell", "input": map[string]any{"command": "true"}},
				map[string]any{"type": "text", "text": "然后继续。"},
			},
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 4, "cache_read_input_tokens": 2, "cache_creation_input_tokens": 0},
		},
	})

	events := readEvents(t, session.Events(), 3)
	assert.Equal(t, core.EventText, events[0].Type)
	assert.Equal(t, "先做两步。", events[0].Content)
	assert.Equal(t, core.EventToolUse, events[1].Type)
	assert.Equal(t, "tool-a", events[1].RequestID)
	assert.Equal(t, core.EventText, events[2].Type)
	assert.Equal(t, "然后继续。", events[2].Content)
	assert.Zero(t, len(session.events), "multi-text complete frame must not re-emit streamed blocks")
	assert.Equal(t, "grok-4.6", state.currentModel)
	assert.Equal(t, 10, state.lastInputTokens)
	assert.Equal(t, 2, state.lastCacheRead)
}

func TestCanonicalizeGrokModelID(t *testing.T) {
	assert.Equal(t, "grok-4.6", canonicalizeGrokModelID("grok-4.6-build"))
	assert.Equal(t, "grok-4.5", canonicalizeGrokModelID("grok-4.5"))
	assert.Equal(t, "grok-4.6", canonicalizeGrokModelID("  grok-4.6-build  "))
	assert.Empty(t, canonicalizeGrokModelID(""))
}

func TestGoldenStreamingMessages(t *testing.T) {
	cases := []struct {
		name       string
		file       string
		wantModel  string
		wantResult string
		wantTools  int
	}{
		{name: "4.5-simple", file: "grok-4.5.ndjson", wantModel: "grok-4.5", wantResult: "PONG-grok-4.5", wantTools: 0},
		{name: "4.6-simple", file: "grok-4.6.ndjson", wantModel: "grok-4.6", wantResult: "PONG-grok-4.6", wantTools: 0},
		{name: "4.5-agentic", file: "agent-grok-4.5.ndjson", wantModel: "grok-4.5", wantResult: "DONE-grok-4.5", wantTools: 2},
		{name: "4.6-agentic", file: "agent-grok-4.6.ndjson", wantModel: "grok-4.6", wantResult: "DONE-grok-4.6", wantTools: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := os.ReadFile(filepath.Join("testdata", tc.file))
			require.NoError(t, err)

			session, err := newGrokSession(context.Background(), sessionConfig{cmd: "grok", workDir: t.TempDir(), mode: "yolo"})
			require.NoError(t, err)
			defer func() { _ = session.Close() }()

			state := newStreamState()
			terminal := false
			for _, line := range strings.Split(strings.TrimSpace(string(payload)), "\n") {
				if strings.TrimSpace(line) == "" {
					continue
				}
				var raw map[string]any
				require.NoError(t, json.Unmarshal([]byte(line), &raw))
				if session.handleEvent(state, raw) {
					terminal = true
				}
			}
			require.True(t, terminal, "golden fixture must end with a terminal result")
			assert.Equal(t, tc.wantModel, state.currentModel)

			events := drainEvents(session)
			var tools int
			var texts []string
			var result string
			for _, event := range events {
				switch event.Type {
				case core.EventToolUse:
					tools++
				case core.EventText:
					if event.Content != "" {
						texts = append(texts, event.Content)
					}
				case core.EventResult:
					result = event.Content
				}
			}
			assert.Equal(t, tc.wantTools, tools)
			assert.Equal(t, tc.wantResult, result)
			require.NotEmpty(t, texts, "expected live text projection")
			// 4.6 agentic emits narration text before tools; ensure it survived.
			if tc.name == "4.6-agentic" {
				joined := strings.Join(texts, "")
				assert.NotEmpty(t, joined)
			}
			assert.Zero(t, len(session.events), "assistant complete must not duplicate streamed events")
		})
	}
}

func drainEvents(session *grokSession) []core.Event {
	var events []core.Event
	for {
		select {
		case event, ok := <-session.Events():
			if !ok {
				return events
			}
			events = append(events, event)
		default:
			return events
		}
	}
}

func TestClipRepeatedTailStopsVerdientLoop(t *testing.T) {
	loop := "正常结尾。 " + strings.Repeat(" verdient", 40)
	clipped, unit, n := clipRepeatedTail(loop)
	require.GreaterOrEqual(t, n, minRepeatCount)
	assert.Equal(t, " verdient", unit)
	assert.NotContains(t, clipped, strings.Repeat(" verdient", minRepeatCount))
	assert.True(t, strings.HasPrefix(loop, clipped))
	assert.Less(t, len(clipped), len(loop)/2)
}

func TestClipRepeatedTailAllowsLongSameCharAndNormalProse(t *testing.T) {
	clipped, unit, n := clipRepeatedTail(strings.Repeat("x", 320*1024))
	assert.Equal(t, 0, n)
	assert.Empty(t, unit)
	assert.Len(t, clipped, 320*1024)

	prose := "今天加仓，不是在赌周一开盘一定涨。系统买的是未来几天期望还是正的。"
	clipped, unit, n = clipRepeatedTail(prose)
	assert.Equal(t, 0, n)
	assert.Empty(t, unit)
	assert.Equal(t, prose, clipped)
}

func TestHandleEventStopsVerdientLoop(t *testing.T) {
	session, err := newGrokSession(context.Background(), sessionConfig{cmd: "grok", workDir: t.TempDir(), mode: "yolo"})
	require.NoError(t, err)
	defer func() { _ = session.Close() }()

	state := newStreamState()
	require.False(t, session.handleEvent(state, map[string]any{"type": "system", "session_id": "sid-loop"}))
	require.False(t, session.handleEvent(state, map[string]any{
		"type": "stream_event",
		"event": map[string]any{
			"type":          "content_block_start",
			"index":         float64(0),
			"content_block": map[string]any{"type": "text", "text": "先说结论。"},
		},
	}))

	terminal := false
	for i := 0; i < 40; i++ {
		terminal = session.handleEvent(state, map[string]any{
			"type": "stream_event",
			"event": map[string]any{
				"type":  "content_block_delta",
				"index": float64(0),
				"delta": map[string]any{"type": "text_delta", "text": " verdient"},
			},
		})
		if terminal {
			break
		}
	}
	assert.True(t, terminal)
	assert.NotEmpty(t, state.runaway)
	assert.Less(t, state.pendingText.Len()+len(state.finalText), 400)

	// Later CLI frames must not keep appending after the trip.
	assert.True(t, session.handleEvent(state, map[string]any{
		"type": "stream_event",
		"event": map[string]any{
			"type":  "content_block_delta",
			"index": float64(0),
			"delta": map[string]any{"type": "text_delta", "text": " verdient"},
		},
	}))

	events := readEvents(t, session.Events(), 3)
	assert.Equal(t, core.EventText, events[0].Type)
	assert.Equal(t, "sid-loop", events[0].SessionID)
	assert.Equal(t, core.EventText, events[1].Type)
	assert.Contains(t, events[1].Content, "先说结论。")
	assert.NotContains(t, events[1].Content, strings.Repeat(" verdient", minRepeatCount))
	assert.Equal(t, core.EventResult, events[2].Type)
	assert.True(t, events[2].Done)
	assert.Contains(t, events[2].Content, "重复输出")
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

func TestSendRedactsSecretsFromInheritedEnvironment(t *testing.T) {
	tests := []struct {
		name       string
		helperMode string
		envKey     string
	}{
		{name: "stderr", helperMode: "fail", envKey: "XAI_API_KEY"},
		{name: "stream error", helperMode: "stream-error", envKey: "GROK_INHERITED_SECRET"},
		{name: "credential", helperMode: "fail", envKey: "SERVICE_CREDENTIAL"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			secret := "inherited-secret-marker-" + strings.ReplaceAll(test.name, " ", "-")
			t.Setenv(test.envKey, secret)
			session := newHelperSession(t, t.TempDir(), test.helperMode, "GROK_HELPER_TARGET_ENV="+test.envKey)
			defer func() { _ = session.Close() }()

			require.NoError(t, session.Send("fail", "", nil, nil))
			events := collectUntilTerminal(t, session.Events())
			require.Len(t, events, 1)
			require.Equal(t, core.EventError, events[0].Type)
			require.Error(t, events[0].Error)
			message := events[0].Error.Error()
			assert.Contains(t, message, test.envKey)
			assert.NotContains(t, message, secret)
			assert.Contains(t, message, "[REDACTED]")
		})
	}
}

func TestSendDoesNotLogNonJSONStdout(t *testing.T) {
	secret := "non-json-credential-marker"
	t.Setenv("SERVICE_CREDENTIAL", secret)
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	session := newHelperSession(t, t.TempDir(), "non-json-warning")
	defer func() { _ = session.Close() }()
	require.NoError(t, session.Send("warn", "", nil, nil))
	events := collectUntilTerminal(t, session.Events())
	require.NotEmpty(t, events)
	require.Equal(t, core.EventResult, events[len(events)-1].Type)
	assert.Contains(t, logs.String(), "ignored non-JSON stdout")
	assert.NotContains(t, logs.String(), secret)
	assert.NotContains(t, logs.String(), "SERVICE_CREDENTIAL")
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
	assert.NotContains(t, cancelEvent.Error.Error(), "process-tree cleanup incomplete")
	assert.True(t, session.Alive(), "CancelTurn must not destroy conversation continuity")
}

func TestCancelTurnReportsAndRedactsProcessTreeCleanupFailure(t *testing.T) {
	const secret = "cleanup-secret-marker"
	t.Setenv("SERVICE_CREDENTIAL", secret)

	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	session := newHelperSession(t, t.TempDir(), "sleep")
	defer func() { _ = session.Close() }()
	cleanupCalled := make(chan struct{}, 1)
	session.forceKill = func(cmd *exec.Cmd) error {
		select {
		case cleanupCalled <- struct{}{}:
		default:
		}
		if cmd == nil || cmd.Process == nil {
			return errors.New("simulated process-tree cleanup failed before process start")
		}
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("simulated direct process kill failed: %w", err)
		}
		return fmt.Errorf("simulated cleanup failure SERVICE_CREDENTIAL=%s", secret)
	}

	require.NoError(t, session.Send("sleep", "", nil, nil))
	initEvent := readEvents(t, session.Events(), 1)[0]
	require.Equal(t, "sid-sleep", initEvent.SessionID)
	require.NoError(t, session.CancelTurn())

	cancelEvent := readEvents(t, session.Events(), 1)[0]
	require.Equal(t, core.EventError, cancelEvent.Type)
	require.Error(t, cancelEvent.Error)
	assert.ErrorIs(t, cancelEvent.Error, context.Canceled)
	message := cancelEvent.Error.Error()
	assert.Contains(t, message, "turn stopped")
	assert.Contains(t, message, "process-tree cleanup incomplete")
	assert.Contains(t, message, "SERVICE_CREDENTIAL=[REDACTED]")
	assert.NotContains(t, message, secret)

	select {
	case <-cleanupCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("injected process-tree cleanup was not called")
	}
	logText := logs.String()
	assert.Contains(t, logText, "process-tree cleanup incomplete")
	assert.Contains(t, logText, "SERVICE_CREDENTIAL=[REDACTED]")
	assert.NotContains(t, logText, secret)
	assert.True(t, session.Alive(), "cleanup failure must not destroy conversation continuity")
}

func TestSendRegistersCancellationBeforePromptPreparation(t *testing.T) {
	workDir := t.TempDir()
	session := newHelperSession(t, workDir, "sleep")
	defer func() { _ = session.Close() }()

	// Holding this mutex stops Send exactly where it publishes the active
	// turn's cancel function. Prompt preparation must not run before that
	// publication, otherwise a concurrent CancelTurn can be lost.
	session.turnCancelMu.Lock()
	sendDone := make(chan error, 1)
	go func() {
		sendDone <- session.Send("sleep", "", nil, nil)
	}()
	turnLocked := waitForGrokTurnLocked(session, 2*time.Second)
	promptPreparedEarly := false
	if turnLocked {
		promptPreparedEarly = waitForGrokPromptFile(workDir, 250*time.Millisecond)
	}
	session.turnCancelMu.Unlock()

	require.True(t, turnLocked, "Send never entered turn preparation")
	require.False(t, promptPreparedEarly, "Send prepared a prompt before publishing its cancel function")
	require.Eventually(t, func() bool {
		session.turnCancelMu.Lock()
		cancelReady := session.turnCancel != nil
		session.turnCancelMu.Unlock()
		return cancelReady
	}, 2*time.Second, 5*time.Millisecond)
	require.NoError(t, session.CancelTurn())

	select {
	case <-sendDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Send did not return after cancellation during preparation")
	}
}

func TestCancelTurnReleasesReaderWhenEventBufferIsFull(t *testing.T) {
	session := newHelperSession(t, t.TempDir(), "sleep")
	defer func() { _ = session.Close() }()
	fillGrokEventBuffer(session)

	require.NoError(t, session.Send("sleep", "", nil, nil))
	require.Eventually(t, func() bool {
		return session.CurrentSessionID() == "sid-sleep"
	}, 2*time.Second, 5*time.Millisecond, "reader never reached the blocked event emission")
	require.NoError(t, session.CancelTurn())
	requireGrokTurnReleased(t, session, 2*time.Second)
	requireGrokStoppedEvent(t, session, 2*time.Second)
}

func TestTurnTimeoutReleasesReaderWhenEventBufferIsFull(t *testing.T) {
	session := newHelperSession(t, t.TempDir(), "sleep")
	defer func() { _ = session.Close() }()
	session.timeout = time.Second
	fillGrokEventBuffer(session)

	require.NoError(t, session.Send("sleep", "", nil, nil))
	require.Eventually(t, func() bool {
		return session.CurrentSessionID() == "sid-sleep"
	}, 2*time.Second, 5*time.Millisecond, "reader never reached the blocked event emission")
	requireGrokTurnReleased(t, session, 3*time.Second)
	requireGrokStoppedEvent(t, session, 2*time.Second)
}

func TestCancelTurnWhileTerminalEventIsBackpressuredEmitsStopped(t *testing.T) {
	for _, helperMode := range []string{"terminal-result", "terminal-error"} {
		t.Run(helperMode, func(t *testing.T) {
			session := newHelperSession(t, t.TempDir(), helperMode)
			defer func() { _ = session.Close() }()
			fillGrokEventBuffer(session)

			require.NoError(t, session.Send("terminal", "", nil, nil))
			require.Eventually(t, func() bool {
				return session.CurrentSessionID() == "sid-terminal"
			}, 2*time.Second, 5*time.Millisecond, "reader never reached the blocked terminal event")
			require.NoError(t, session.CancelTurn())
			requireGrokTurnReleased(t, session, 2*time.Second)
			requireGrokStoppedEvent(t, session, 2*time.Second)
		})
	}
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
		target := os.Getenv("GROK_HELPER_TARGET_ENV")
		if target == "" {
			target = "XAI_API_KEY"
		}
		_, _ = fmt.Fprintf(os.Stderr, "\x1b[31mfailed using %s=%s\x1b[0m\n", target, os.Getenv(target))
		os.Exit(9)
	case "stream-error":
		target := os.Getenv("GROK_HELPER_TARGET_ENV")
		if target == "" {
			target = "GROK_INHERITED_SECRET"
		}
		helperWriteJSON(map[string]any{
			"type":    "error",
			"message": target + "=" + os.Getenv(target),
		})
		os.Exit(0)
	case "non-json-warning":
		_, _ = fmt.Fprintf(os.Stdout, "warning SERVICE_CREDENTIAL=%s\n", os.Getenv("SERVICE_CREDENTIAL"))
		helperWriteJSON(map[string]any{"type": "system", "session_id": "sid-warning"})
		helperWriteJSON(map[string]any{"type": "result", "subtype": "success", "result": "ok", "session_id": "sid-warning"})
		os.Exit(0)
	case "terminal-result":
		helperWriteJSON(map[string]any{"type": "result", "subtype": "success", "result": "ok", "session_id": "sid-terminal"})
		os.Exit(0)
	case "terminal-error":
		helperWriteJSON(map[string]any{"type": "error", "message": "terminal failure", "session_id": "sid-terminal"})
		os.Exit(0)
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

func waitForGrokPromptFile(workDir string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	pattern := filepath.Join(workDir, ".cc-connect", "prompts", "grok-prompt-*.txt")
	for time.Now().Before(deadline) {
		if matches, _ := filepath.Glob(pattern); len(matches) > 0 {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func waitForGrokTurnLocked(session *grokSession, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if session.turnMu.TryLock() {
			session.turnMu.Unlock()
			time.Sleep(5 * time.Millisecond)
			continue
		}
		return true
	}
	return false
}

func fillGrokEventBuffer(session *grokSession) {
	for range cap(session.events) {
		session.events <- core.Event{Type: core.EventText, Content: "buffered"}
	}
}

func requireGrokTurnReleased(t *testing.T, session *grokSession, timeout time.Duration) {
	t.Helper()
	released := make(chan struct{})
	go func() {
		session.turnMu.Lock()
		defer session.turnMu.Unlock()
		close(released)
	}()
	select {
	case <-released:
	case <-time.After(timeout):
		t.Fatal("turn mutex remained locked after cancellation")
	}
}

func requireGrokStoppedEvent(t *testing.T, session *grokSession, timeout time.Duration) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case event := <-session.events:
			if event.Type == core.EventError && event.Error != nil && strings.Contains(event.Error.Error(), "turn stopped") {
				return
			}
		case <-timer.C:
			t.Fatal("missing terminal stopped-turn event after cancellation")
		}
	}
}
