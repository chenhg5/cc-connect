package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestAppServerSession_ApplyThreadRuntimeState(t *testing.T) {
	s := &appServerSession{}
	effort := "xhigh"

	s.applyThreadRuntimeState("/tmp/project", "gpt-5.4", &effort)

	if got := s.GetWorkDir(); got != "/tmp/project" {
		t.Fatalf("GetWorkDir() = %q, want /tmp/project", got)
	}
	if got := s.GetModel(); got != "gpt-5.4" {
		t.Fatalf("GetModel() = %q, want gpt-5.4", got)
	}
	if got := s.GetReasoningEffort(); got != "xhigh" {
		t.Fatalf("GetReasoningEffort() = %q, want xhigh", got)
	}
}

func TestAppServerSession_HandleRateLimitsUpdatedCachesUsage(t *testing.T) {
	s := &appServerSession{}
	raw, err := json.Marshal(appServerRateLimitsResponse{
		RateLimits: appServerRateLimitSnapshot{
			LimitID:   "codex",
			PlanType:  "pro",
			Primary:   &appServerRateLimitWindow{UsedPercent: 25, WindowDurationMins: 15, ResetsAt: 1730947200},
			Secondary: &appServerRateLimitWindow{UsedPercent: 42, WindowDurationMins: 60, ResetsAt: 1730950800},
			Credits:   &appServerCreditsSnapshot{HasCredits: true, Unlimited: false},
		},
	})
	if err != nil {
		t.Fatalf("marshal notification: %v", err)
	}

	s.handleNotification("account/rateLimits/updated", raw)

	report, err := s.GetUsage(context.Background())
	if err != nil {
		t.Fatalf("GetUsage() returned error: %v", err)
	}
	if report.Provider != "codex" {
		t.Fatalf("provider = %q, want codex", report.Provider)
	}
	if report.Plan != "pro" {
		t.Fatalf("plan = %q, want pro", report.Plan)
	}
	if len(report.Buckets) != 1 {
		t.Fatalf("buckets = %d, want 1", len(report.Buckets))
	}
	if got := report.Buckets[0].Name; got != "codex" {
		t.Fatalf("bucket name = %q, want codex", got)
	}
	if got := report.Buckets[0].Windows[0].WindowSeconds; got != 15*60 {
		t.Fatalf("primary window seconds = %d, want %d", got, 15*60)
	}
	if got := report.Buckets[0].Windows[1].UsedPercent; got != 42 {
		t.Fatalf("secondary used percent = %d, want 42", got)
	}
	if report.Credits == nil || !report.Credits.HasCredits {
		t.Fatalf("credits = %#v, want has credits", report.Credits)
	}
}

func TestAppServerSession_HandleThreadTokenUsageUpdatedCachesContextUsage(t *testing.T) {
	s := &appServerSession{}
	raw, err := json.Marshal(appServerThreadTokenUsageNotification{
		ThreadID: "thread-1",
		TurnID:   "turn-1",
		TokenUsage: struct {
			Total              codexTokenUsage `json:"total"`
			Last               codexTokenUsage `json:"last"`
			ModelContextWindow int             `json:"modelContextWindow"`
		}{
			Total: codexTokenUsage{
				TotalTokens:           52011395,
				InputTokens:           51847383,
				CachedInputTokens:     48187904,
				OutputTokens:          164012,
				ReasoningOutputTokens: 78910,
			},
			Last: codexTokenUsage{
				TotalTokens:           41061,
				InputTokens:           40849,
				CachedInputTokens:     36864,
				OutputTokens:          212,
				ReasoningOutputTokens: 32,
			},
			ModelContextWindow: 258400,
		},
	})
	if err != nil {
		t.Fatalf("marshal notification: %v", err)
	}

	s.handleNotification("thread/tokenUsage/updated", raw)

	usage := s.GetContextUsage()
	if usage == nil {
		t.Fatal("GetContextUsage() = nil, want cached context usage")
	}
	if usage.UsedTokens != 41061 {
		t.Fatalf("used tokens = %d, want 41061", usage.UsedTokens)
	}
	if usage.BaselineTokens != codexContextBaselineTokens {
		t.Fatalf("baseline tokens = %d, want %d", usage.BaselineTokens, codexContextBaselineTokens)
	}
	if usage.TotalTokens != 41061 {
		t.Fatalf("total tokens = %d, want 41061", usage.TotalTokens)
	}
	if usage.ContextWindow != 258400 {
		t.Fatalf("context window = %d, want 258400", usage.ContextWindow)
	}
	if usage.CachedInputTokens != 36864 {
		t.Fatalf("cached input tokens = %d, want 36864", usage.CachedInputTokens)
	}
	if usage.InputTokens != 40849 {
		t.Fatalf("input tokens = %d, want 40849", usage.InputTokens)
	}
}

func TestAppServerSession_IgnoresSubagentLifecycleNotifications(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 8)}
	s.threadID.Store("parent-thread")
	s.currentTurn = "parent-turn"

	s.handleNotification("turn/started", notificationProbe(t, map[string]any{
		"threadId": "child-thread",
		"turn":     map[string]any{"id": "child-turn", "status": "inProgress"},
	}))
	if got := appServerCurrentTurn(s); got != "parent-turn" {
		t.Fatalf("current turn = %q after child turn started, want parent-turn", got)
	}

	s.handleNotification("item/completed", notificationProbe(t, map[string]any{
		"threadId": "child-thread",
		"turnId":   "child-turn",
		"item":     map[string]any{"type": "agentMessage", "text": "child-only answer"},
	}))
	if got := appServerPendingMessages(s); len(got) != 0 {
		t.Fatalf("pending messages = %v after child item, want none", got)
	}

	s.handleNotification("turn/completed", notificationProbe(t, map[string]any{
		"threadId": "child-thread",
		"turn":     map[string]any{"id": "child-turn", "status": "completed"},
	}))
	s.handleNotification("thread/status/changed", notificationProbe(t, map[string]any{
		"threadId": "child-thread",
		"status":   map[string]any{"type": "idle"},
	}))
	if got := appServerCurrentTurn(s); got != "parent-turn" {
		t.Fatalf("current turn = %q after child completion, want parent-turn", got)
	}
	select {
	case event := <-s.events:
		t.Fatalf("child lifecycle emitted event %#v, want none", event)
	default:
	}

	s.handleNotification("item/completed", notificationProbe(t, map[string]any{
		"threadId": "parent-thread",
		"turnId":   "parent-turn",
		"item":     map[string]any{"type": "agentMessage", "text": "parent answer"},
	}))
	s.handleNotification("turn/completed", notificationProbe(t, map[string]any{
		"threadId": "parent-thread",
		"turn":     map[string]any{"id": "parent-turn", "status": "completed"},
	}))

	textEvent := <-s.events
	if textEvent.Type != core.EventText || textEvent.Content != "parent answer" {
		t.Fatalf("text event = %#v, want parent answer", textEvent)
	}
	resultEvent := <-s.events
	if resultEvent.Type != core.EventResult || !resultEvent.Done {
		t.Fatalf("result event = %#v, want completed parent result", resultEvent)
	}
}

func TestAppServerSession_IgnoresSubagentTokenUsageNotifications(t *testing.T) {
	s := &appServerSession{}
	s.threadID.Store("parent-thread")
	s.currentTurn = "parent-turn"

	s.handleNotification("thread/tokenUsage/updated", tokenUsageNotificationProbe(t, "parent-thread", "parent-turn", 1234))
	s.handleNotification("thread/tokenUsage/updated", tokenUsageNotificationProbe(t, "child-thread", "child-turn", 9876))

	usage := s.GetContextUsage()
	if usage == nil {
		t.Fatal("GetContextUsage() = nil, want parent usage")
	}
	if usage.UsedTokens != 1234 {
		t.Fatalf("used tokens = %d after child update, want parent value 1234", usage.UsedTokens)
	}
}

func TestAppServerSession_FailedTurnEmitsError(t *testing.T) {
	s := &appServerSession{
		events:      make(chan core.Event, 2),
		currentTurn: "turn-1",
		pendingMsgs: []string{"partial reasoning"},
	}

	raw, err := json.Marshal(map[string]any{
		"threadId": "thread-1",
		"turn": map[string]any{
			"id":     "turn-1",
			"status": "failed",
			"error":  map[string]any{"message": "service unavailable"},
		},
	})
	if err != nil {
		t.Fatalf("marshal notification: %v", err)
	}
	s.handleNotification("turn/completed", raw)

	event := <-s.events
	if event.Type != core.EventError || event.Error == nil || event.Error.Error() != "service unavailable" {
		t.Fatalf("event = %#v, want EventError with service unavailable", event)
	}

	s.stateMu.Lock()
	currentTurn := s.currentTurn
	pendingCount := len(s.pendingMsgs)
	s.stateMu.Unlock()
	if currentTurn != "" {
		t.Fatalf("current turn = %q after failure, want empty", currentTurn)
	}
	if pendingCount != 0 {
		t.Fatalf("pending message count = %d after failure, want 0", pendingCount)
	}

	idleRaw, err := json.Marshal(map[string]any{
		"threadId": "thread-1",
		"status":   map[string]any{"type": "idle"},
	})
	if err != nil {
		t.Fatalf("marshal idle notification: %v", err)
	}
	s.handleNotification("thread/status/changed", idleRaw)
	select {
	case duplicate := <-s.events:
		t.Fatalf("idle notification emitted duplicate event %#v", duplicate)
	default:
	}
}

func TestAppServerSession_RequestTimeoutIncludesBlockedStdinWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stdin := newBlockingWriteCloser()
	defer func() { _ = stdin.Close() }()

	s := &appServerSession{
		ctx:     ctx,
		cancel:  cancel,
		events:  make(chan core.Event),
		stdin:   stdin,
		pending: make(map[int64]chan rpcResponseEnvelope),
	}

	done := make(chan error, 1)
	go func() {
		var out map[string]any
		done <- s.requestWithTimeout("turn/start", map[string]any{
			"input": strings.Repeat("x", 1024),
		}, &out, 25*time.Millisecond)
	}()

	select {
	case <-stdin.started:
	case <-time.After(time.Second):
		t.Fatal("request did not attempt to write to stdin")
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("requestWithTimeout returned nil, want write timeout")
		}
		if !strings.Contains(err.Error(), "turn/start") || !strings.Contains(err.Error(), "write timed out") {
			t.Fatalf("error = %q, want turn/start write timeout", err.Error())
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("requestWithTimeout did not return while stdin write was blocked")
	}

	if !stdin.Closed() {
		t.Fatal("blocked stdin was not closed after timeout")
	}
}

func TestAppServerSession_ReadLoopHandlesLargeResponseLine(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := make(chan rpcResponseEnvelope, 1)
	s := &appServerSession{
		ctx:     ctx,
		events:  make(chan core.Event, 1),
		pending: map[int64]chan rpcResponseEnvelope{1: ch},
	}

	var input bytes.Buffer
	input.WriteString(`{"jsonrpc":"2.0","id":1,"result":{"cwd":"/tmp/project","model":"gpt-test","reasoningEffort":"high","thread":{"id":"thread-large"},"initialTurnsPage":{"data":"`)
	input.WriteString(strings.Repeat("x", 11*1024*1024))
	input.WriteString(`"}}}` + "\n")

	s.wg.Add(1)
	go s.readLoop(&input)

	select {
	case resp := <-ch:
		if resp.Error != nil {
			t.Fatalf("response error = %v", resp.Error)
		}
		var decoded threadResumeResponse
		if err := json.Unmarshal(resp.Result, &decoded); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}
		if decoded.Thread.ID != "thread-large" {
			t.Fatalf("thread id = %q, want thread-large", decoded.Thread.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for large response")
	}

	s.wg.Wait()
}

func TestAppServerSession_ResumeDoesNotRequestExtendedHistory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stdin := &lockedWriteCloser{}
	s := &appServerSession{
		ctx:     ctx,
		stdin:   stdin,
		pending: make(map[int64]chan rpcResponseEnvelope),
	}

	done := make(chan error, 1)
	go func() {
		done <- s.ensureThread("thread-existing")
	}()

	line := waitForWrittenJSONLine(t, stdin)
	var request struct {
		ID     int64          `json:"id"`
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal([]byte(line), &request); err != nil {
		t.Fatalf("decode request %q: %v", line, err)
	}
	if request.Method != "thread/resume" {
		t.Fatalf("method = %q, want thread/resume", request.Method)
	}
	if got := request.Params["threadId"]; got != "thread-existing" {
		t.Fatalf("threadId = %#v, want thread-existing", got)
	}
	if got := request.Params["persistExtendedHistory"]; got != false {
		t.Fatalf("persistExtendedHistory = %#v, want false", got)
	}

	s.handleResponse(rpcResponseEnvelope{
		ID: request.ID,
		Result: json.RawMessage(
			`{"cwd":"/tmp/project","model":"gpt-test","thread":{"id":"thread-existing"}}`,
		),
	})

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ensureThread() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for thread resume")
	}
}

func TestMapAppServerRateLimits_PrefersMultiBucketView(t *testing.T) {
	report := mapAppServerRateLimits(appServerRateLimitsResponse{
		RateLimits: appServerRateLimitSnapshot{
			LimitID:  "legacy",
			PlanType: "team",
			Primary:  &appServerRateLimitWindow{UsedPercent: 99, WindowDurationMins: 15},
		},
		RateLimitsByLimitID: map[string]appServerRateLimitSnapshot{
			"codex": {
				LimitID:   "codex",
				LimitName: "Codex",
				PlanType:  "team",
				Primary:   &appServerRateLimitWindow{UsedPercent: 10, WindowDurationMins: 15},
			},
			"codex_other": {
				LimitID:  "codex_other",
				PlanType: "team",
				Primary:  &appServerRateLimitWindow{UsedPercent: 20, WindowDurationMins: 60},
			},
		},
	})

	if report.Plan != "team" {
		t.Fatalf("plan = %q, want team", report.Plan)
	}
	if len(report.Buckets) != 2 {
		t.Fatalf("buckets = %d, want 2", len(report.Buckets))
	}
	if report.Buckets[0].Name != "Codex" {
		t.Fatalf("first bucket = %q, want Codex", report.Buckets[0].Name)
	}
	if report.Buckets[1].Name != "codex_other" {
		t.Fatalf("second bucket = %q, want codex_other", report.Buckets[1].Name)
	}
}

func TestAppServerSession_HandleRequestUserInputEmitsAskQuestion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stdin := &lockedWriteCloser{}
	s := &appServerSession{
		events:           make(chan core.Event, 4),
		ctx:              ctx,
		pendingApprovals: make(map[string]chan core.PermissionResult),
		stdin:            stdin,
	}

	s.handleServerRequest(serverRequestProbe(t, `"rui-1"`, "item/tool/requestUserInput", map[string]any{
		"threadId": "thread-1",
		"turnId":   "turn-1",
		"itemId":   "call-1",
		"questions": []any{
			map[string]any{
				"id":       "database",
				"header":   "Database",
				"question": "Which database should we use?",
				"isOther":  true,
				"isSecret": false,
				"options": []any{
					map[string]any{"label": "Postgres", "description": "Use the existing relational database"},
					map[string]any{"label": "SQLite", "description": "Keep it embedded"},
				},
			},
		},
	}))

	var event core.Event
	select {
	case event = <-s.events:
	case <-time.After(time.Second):
		t.Fatal("expected AskUserQuestion event")
	}
	if event.Type != core.EventPermissionRequest {
		t.Fatalf("event type = %s, want %s", event.Type, core.EventPermissionRequest)
	}
	if event.ToolName != "AskUserQuestion" {
		t.Fatalf("tool name = %q, want AskUserQuestion", event.ToolName)
	}
	if event.RequestID != `"rui-1"` {
		t.Fatalf("request id = %q, want raw JSON id", event.RequestID)
	}
	if len(event.Questions) != 1 {
		t.Fatalf("questions = %d, want 1", len(event.Questions))
	}
	q := event.Questions[0]
	if q.Question != "Which database should we use?" || q.Header != "Database" {
		t.Fatalf("question = %#v", q)
	}
	if len(q.Options) != 2 || q.Options[0].Label != "Postgres" || q.Options[1].Description != "Keep it embedded" {
		t.Fatalf("options = %#v", q.Options)
	}
	if stdin.String() != "" {
		t.Fatalf("request_user_input should not write before the answer, got %q", stdin.String())
	}
}

func TestAppServerSession_HandleRequestUserInputWritesCodexResponse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stdin := &lockedWriteCloser{}
	s := &appServerSession{
		events:           make(chan core.Event, 4),
		ctx:              ctx,
		pendingApprovals: make(map[string]chan core.PermissionResult),
		stdin:            stdin,
	}

	s.handleServerRequest(serverRequestProbe(t, `"rui-2"`, "item/tool/requestUserInput", map[string]any{
		"threadId": "thread-1",
		"turnId":   "turn-1",
		"itemId":   "call-2",
		"questions": []any{
			map[string]any{
				"id":       "database",
				"header":   "Database",
				"question": "Which database should we use?",
				"options": []any{
					map[string]any{"label": "Postgres", "description": "Use the existing relational database"},
					map[string]any{"label": "SQLite", "description": "Keep it embedded"},
				},
			},
		},
	}))

	var event core.Event
	select {
	case event = <-s.events:
	case <-time.After(time.Second):
		t.Fatal("expected AskUserQuestion event")
	}
	if err := s.RespondPermission(event.RequestID, core.PermissionResult{
		Behavior: "allow",
		UpdatedInput: map[string]any{
			"answers": map[string]any{
				"Which database should we use?": "Postgres",
			},
		},
	}); err != nil {
		t.Fatalf("RespondPermission() error = %v", err)
	}

	line := waitForWrittenJSONLine(t, stdin)
	var envelope struct {
		JSONRPC string `json:"jsonrpc"`
		ID      string `json:"id"`
		Result  struct {
			Answers map[string]struct {
				Answers []string `json:"answers"`
			} `json:"answers"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(line), &envelope); err != nil {
		t.Fatalf("decode response %q: %v", line, err)
	}
	if envelope.JSONRPC != "2.0" || envelope.ID != "rui-2" {
		t.Fatalf("envelope = %#v", envelope)
	}
	got := envelope.Result.Answers["database"].Answers
	if len(got) != 1 || got[0] != "Postgres" {
		t.Fatalf("answers[database] = %#v, want [Postgres]", got)
	}
}

func TestAppServerSession_RejectsInterleavedChildServerRequests(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdin := &lockedWriteCloser{}
	s := &appServerSession{
		ctx: ctx, stdin: stdin, events: make(chan core.Event, 8),
		pendingApprovals: make(map[string]chan core.PermissionResult),
		currentTurn:      "parent-turn",
	}
	s.threadID.Store("parent-thread")

	request := func(id, method, threadID, turnID string) {
		t.Helper()
		s.handleServerRequest(serverRequestProbe(t, `"`+id+`"`, method, map[string]any{
			"threadId": threadID, "turnId": turnID,
			"command": "echo child", "permissions": map[string]any{"network": true},
			"questions": []any{map[string]any{"id": "q", "header": "Q", "question": "Child question?"}},
		}))
	}

	request("child-1-command", "item/commandExecution/requestApproval", "child-thread-1", "child-turn-1")
	request("parent-command", "item/commandExecution/requestApproval", "parent-thread", "parent-turn")
	request("child-2-file", "item/fileChange/requestApproval", "child-thread-2", "child-turn-2")
	request("child-2-permissions", "item/permissions/requestApproval", "child-thread-2", "child-turn-2")
	request("child-3-question", "item/tool/requestUserInput", "child-thread-3", "child-turn-3")
	request("child-3-tool", "item/tool/call", "child-thread-3", "child-turn-3")
	request("stale-parent-turn", "item/commandExecution/requestApproval", "parent-thread", "old-turn")

	select {
	case event := <-s.events:
		if event.Type != core.EventPermissionRequest || event.RequestID != `"parent-command"` || event.ToolName != "Bash" {
			t.Fatalf("event = %#v, want only parent Bash approval", event)
		}
		if err := s.RespondPermission(event.RequestID, core.PermissionResult{Behavior: "allow"}); err != nil {
			t.Fatalf("respond to parent approval: %v", err)
		}
	default:
		t.Fatal("parent approval was not emitted")
	}
	select {
	case event := <-s.events:
		t.Fatalf("child request leaked event %#v", event)
	default:
	}
	if got := appServerCurrentTurn(s); got != "parent-turn" {
		t.Fatalf("current turn = %q, want parent-turn", got)
	}

	var responses map[string]map[string]json.RawMessage
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		responses = make(map[string]map[string]json.RawMessage)
		for _, line := range strings.Split(strings.TrimSpace(stdin.String()), "\n") {
			if line == "" {
				continue
			}
			var response struct {
				ID     string                     `json:"id"`
				Result map[string]json.RawMessage `json:"result"`
			}
			if err := json.Unmarshal([]byte(line), &response); err != nil {
				t.Fatalf("decode response %q: %v", line, err)
			}
			responses[response.ID] = response.Result
		}
		if len(responses) == 7 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(responses) != 7 {
		t.Fatalf("responses = %v, want all seven requests answered", responses)
	}
	for _, id := range []string{"child-1-command", "child-2-file", "stale-parent-turn"} {
		if string(responses[id]["decision"]) != `"decline"` {
			t.Fatalf("%s response = %s, want decline", id, responses[id]["decision"])
		}
	}
	if string(responses["parent-command"]["decision"]) != `"accept"` {
		t.Fatalf("parent response = %s, want accept", responses["parent-command"]["decision"])
	}
	if string(responses["child-2-permissions"]["permissions"]) != `{}` {
		t.Fatalf("child permissions response = %s, want empty permissions", responses["child-2-permissions"]["permissions"])
	}
	if string(responses["child-3-question"]["answers"]) != `{}` {
		t.Fatalf("child question response = %s, want empty answers", responses["child-3-question"]["answers"])
	}
	if string(responses["child-3-tool"]["success"]) != `false` {
		t.Fatalf("child tool response = %s, want unavailable", responses["child-3-tool"]["success"])
	}
	s.approvalsMu.Lock()
	pendingCount := len(s.pendingApprovals)
	s.approvalsMu.Unlock()
	if pendingCount != 0 {
		t.Fatalf("pending approvals = %d, want none", pendingCount)
	}
}

var _ interface {
	GetUsage(context.Context) (*core.UsageReport, error)
} = (*appServerSession)(nil)

var _ interface {
	GetContextUsage() *core.ContextUsage
} = (*appServerSession)(nil)

type lockedWriteCloser struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *lockedWriteCloser) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *lockedWriteCloser) Close() error { return nil }

func (w *lockedWriteCloser) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

var _ io.WriteCloser = (*lockedWriteCloser)(nil)

func newTransportTestSession(t *testing.T) *appServerSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s := &appServerSession{
		ctx: ctx, cancel: cancel, events: make(chan core.Event, 128),
		currentTurn: "turn-1",
	}
	s.threadID.Store("thread-1")
	s.alive.Store(true)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func awaitTransportTest(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("app-server operation did not finish")
	}
}

func TestAppServerSession_TransportEOFFinishesActiveTurn(t *testing.T) {
	s := newTransportTestSession(t)
	rpc := make(chan rpcResponseEnvelope, 1)
	approval := make(chan core.PermissionResult, 1)
	s.pending = map[int64]chan rpcResponseEnvelope{1: rpc}
	s.pendingApprovals = map[string]chan core.PermissionResult{"approval": approval}
	s.wg.Add(1)
	s.readLoop(strings.NewReader(""))
	select {
	case event := <-s.events:
		if event.Type != core.EventError || !errors.Is(event.Error, io.EOF) {
			t.Fatalf("EOF event = %#v, want terminal EOF error", event)
		}
	default:
		t.Fatal("EOF left the active turn waiting without a terminal event")
	}
	if s.Alive() || s.currentTurn != "" {
		t.Fatal("EOF left the transport or turn active")
	}
	select {
	case response := <-rpc:
		if response.Error == nil {
			t.Fatal("pending RPC was not rejected")
		}
	default:
		t.Fatal("pending RPC was not released")
	}
	select {
	case result := <-approval:
		if result.Behavior != "deny" {
			t.Fatalf("pending approval = %#v", result)
		}
	default:
		t.Fatal("pending approval was not released")
	}
}

func TestAppServerSession_TransportEOFDoesNotDuplicateTerminal(t *testing.T) {
	for _, state := range []string{"idle", "completed", "failed", "error", "canceled"} {
		t.Run(state, func(t *testing.T) {
			s := newTransportTestSession(t)
			switch state {
			case "idle":
				s.currentTurn = ""
			case "completed":
				s.handleNotification("turn/completed", json.RawMessage(`{"turn":{"id":"turn-1"}}`))
			case "failed":
				s.handleNotification("turn/completed", json.RawMessage(`{"turn":{"id":"turn-1","status":"failed"}}`))
			case "error":
				s.handleNotification("error", json.RawMessage(`{"message":"failed"}`))
			case "canceled":
				s.cancel()
			}
			before := len(s.events)
			s.wg.Add(1)
			s.readLoop(strings.NewReader(""))
			if len(s.events) != before {
				t.Fatalf("EOF emitted an extra terminal event after %s", state)
			}
		})
	}
}

func TestAppServerSession_TransportReadFailureFinishesTurn(t *testing.T) {
	s := newTransportTestSession(t)
	failure := errors.New("broken transport")
	s.wg.Add(1)
	s.readLoop(iotest.ErrReader(failure))
	select {
	case event := <-s.events:
		if event.Type != core.EventError || !errors.Is(event.Error, failure) {
			t.Fatalf("read failure = %#v", event)
		}
	default:
		t.Fatal("read failure did not finish the turn")
	}
	if s.Alive() || s.currentTurn != "" {
		t.Fatal("read failure left the transport or turn active")
	}
}

func TestAppServerSession_TransportIdleErrorRemainsVisible(t *testing.T) {
	s := newTransportTestSession(t)
	s.currentTurn = ""
	s.handleNotification("error", json.RawMessage(`{"message":"connection error"}`))
	select {
	case event := <-s.events:
		if event.Type != core.EventError || event.Error.Error() != "connection error" {
			t.Fatalf("idle error = %#v", event)
		}
	default:
		t.Fatal("connection error outside a turn was suppressed")
	}
}

func TestAppServerSession_TerminalSurvivesFullQueue(t *testing.T) {
	for _, terminal := range []core.Event{
		{Type: core.EventResult, Done: true},
		{Type: core.EventError, Error: io.EOF},
		{Type: core.EventText, Done: true},
	} {
		t.Run(string(terminal.Type), func(t *testing.T) {
			s := newTransportTestSession(t)
			for i := 0; i < cap(s.events); i++ {
				s.events <- core.Event{Type: core.EventThinking, Content: "queued"}
			}
			done := make(chan struct{})
			go func() { s.emit(terminal); close(done) }()
			select {
			case <-done:
				t.Fatal("terminal delivery returned while the queue was full")
			case <-time.After(20 * time.Millisecond):
			}
			for i := 0; i < cap(s.events); i++ {
				if event := <-s.events; event.Type != core.EventThinking || event.Content != "queued" {
					t.Fatalf("queued event %d was displaced: %#v", i, event)
				}
			}
			awaitTransportTest(t, done)
			select {
			case event := <-s.events:
				if event.Type != terminal.Type || event.Done != terminal.Done || event.Error != terminal.Error {
					t.Fatalf("terminal = %#v, want %#v", event, terminal)
				}
			default:
				t.Fatal("full queue dropped the terminal event")
			}
		})
	}
}

func TestAppServerSession_CloseUnblocksTerminalDelivery(t *testing.T) {
	s := newTransportTestSession(t)
	for i := 0; i < cap(s.events); i++ {
		s.events <- core.Event{Type: core.EventThinking}
	}
	done := make(chan struct{})
	s.wg.Add(1)
	go func() { defer s.wg.Done(); s.emitError(io.EOF); close(done) }()
	select {
	case <-done:
		t.Fatal("terminal delivery returned before cancellation or queue space")
	case <-time.After(20 * time.Millisecond):
	}
	closed := make(chan struct{})
	go func() { _ = s.Close(); close(closed) }()
	awaitTransportTest(t, done)
	awaitTransportTest(t, closed)
	// A publisher outside readLoop may race with or follow Close.
	s.emitError(io.EOF)
}

type transportTestWriter func([]byte) (int, error)

func (w transportTestWriter) Write(p []byte) (int, error) { return w(p) }
func (transportTestWriter) Close() error                  { return nil }

func TestAppServerSession_LateStartResponseCannotReviveTurn(t *testing.T) {
	for _, state := range []string{"eof", "completed", "completed_then_eof", "failed"} {
		t.Run(state, func(t *testing.T) {
			s := newTransportTestSession(t)
			s.currentTurn = ""
			s.stdin = transportTestWriter(func(p []byte) (int, error) {
				var request struct {
					ID int64 `json:"id"`
				}
				if err := json.Unmarshal(p, &request); err != nil {
					return 0, err
				}
				s.handleResponse(rpcResponseEnvelope{ID: request.ID, Result: json.RawMessage(`{"turn":{"id":"turn-1"}}`)})
				if state != "eof" {
					s.handleNotification("turn/started", json.RawMessage(`{"turn":{"id":"turn-1"}}`))
					status := "completed"
					if state == "failed" {
						status = "failed"
					}
					s.handleNotification("turn/completed", json.RawMessage(fmt.Sprintf(`{"turn":{"id":"turn-1","status":%q}}`, status)))
				}
				if strings.Contains(state, "eof") {
					s.wg.Add(1)
					s.readLoop(strings.NewReader(""))
				}
				return len(p), nil
			})
			err := s.Send("hello", "", nil, nil)
			if state == "eof" && err == nil {
				t.Fatal("turn/start succeeded after EOF without a terminal event")
			}
			if state != "eof" && err != nil {
				t.Fatalf("finished turn became a send failure: %v", err)
			}
			if s.currentTurn != "" {
				t.Fatal("late turn/start response revived the finished turn")
			}
		})
	}
}

func TestAppServerSession_EOFReleasesRPCBeforeTerminalBackpressure(t *testing.T) {
	s := newTransportTestSession(t)
	for i := 0; i < cap(s.events); i++ {
		s.events <- core.Event{Type: core.EventThinking}
	}
	rpc := make(chan rpcResponseEnvelope, 1)
	approval := make(chan core.PermissionResult, 1)
	s.pending = map[int64]chan rpcResponseEnvelope{1: rpc}
	s.pendingApprovals = map[string]chan core.PermissionResult{"approval": approval}
	done := make(chan struct{})
	s.wg.Add(1)
	go func() { s.readLoop(strings.NewReader("")); close(done) }()
	select {
	case response := <-rpc:
		if response.Error == nil || s.Alive() {
			t.Fatal("RPC did not observe a dead transport")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("terminal backpressure prevented pending RPC rejection")
	}
	select {
	case <-approval:
	case <-time.After(5 * time.Second):
		t.Fatal("terminal backpressure prevented approval rejection")
	}
	for i := 0; i < cap(s.events); i++ {
		<-s.events
	}
	awaitTransportTest(t, done)
	select {
	case event := <-s.events:
		if event.Type != core.EventError || !errors.Is(event.Error, io.EOF) {
			t.Fatalf("EOF terminal = %#v", event)
		}
	default:
		t.Fatal("EOF terminal was lost under backpressure")
	}
}

// Use connect's real pipes and process lifecycle without invoking Codex.
func TestAppServerSession_ProcessExitFinishesOnce(t *testing.T) {
	for _, mode := range []string{"eof", "completed", "failed", "completed_then_failed_exit"} {
		t.Run(mode, func(t *testing.T) {
			s := newTransportTestSession(t)
			s.cliBin = os.Args[0]
			s.cliExtraArgs = []string{"-test.run=^TestAppServerTransportProcessHelper$", "--"}
			s.workDir = t.TempDir()
			s.url = "stdio://"
			s.extraEnv = []string{"CC_CONNECT_TRANSPORT_HELPER=" + mode, "GORACE=atexit_sleep_ms=0"}
			if err := s.connect(); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() { s.wg.Wait(); close(done) }()
			awaitTransportTest(t, done)
			want := core.EventError
			if strings.HasPrefix(mode, "completed") {
				want = core.EventResult
			}
			if len(s.events) != 1 {
				t.Fatalf("process exit produced %d events, want one terminal", len(s.events))
			}
			if event := <-s.events; event.Type != want {
				t.Fatalf("process exit event = %#v, want %v", event, want)
			}
			if s.Alive() || s.currentTurn != "" {
				t.Fatal("process exit left the transport or turn active")
			}
		})
	}
}

func TestAppServerTransportProcessHelper(t *testing.T) {
	mode := os.Getenv("CC_CONNECT_TRANSPORT_HELPER")
	if mode == "" {
		return
	}
	if strings.HasPrefix(mode, "completed") {
		if _, err := fmt.Fprintln(os.Stdout, `{"method":"turn/completed","params":{"turn":{"id":"turn-1"}}}`); err != nil {
			t.Fatal(err)
		}
	}
	if mode == "failed" || mode == "completed_then_failed_exit" {
		os.Exit(17)
	}
	os.Exit(0)
}

type blockingWriteCloser struct {
	started   chan struct{}
	closed    chan struct{}
	closeOnce sync.Once

	mu       sync.Mutex
	isClosed bool
}

func newBlockingWriteCloser() *blockingWriteCloser {
	return &blockingWriteCloser{
		started: make(chan struct{}),
		closed:  make(chan struct{}),
	}
}

func (w *blockingWriteCloser) Write([]byte) (int, error) {
	select {
	case <-w.started:
	default:
		close(w.started)
	}
	<-w.closed
	return 0, io.ErrClosedPipe
}

func (w *blockingWriteCloser) Close() error {
	w.closeOnce.Do(func() {
		w.mu.Lock()
		w.isClosed = true
		w.mu.Unlock()
		close(w.closed)
	})
	return nil
}

func (w *blockingWriteCloser) Closed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.isClosed
}

var _ io.WriteCloser = (*blockingWriteCloser)(nil)

func serverRequestProbe(t *testing.T, idJSON, method string, params any) map[string]json.RawMessage {
	t.Helper()
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	methodJSON, err := json.Marshal(method)
	if err != nil {
		t.Fatalf("marshal method: %v", err)
	}
	return map[string]json.RawMessage{
		"id":     json.RawMessage(idJSON),
		"method": methodJSON,
		"params": paramsJSON,
	}
}

func notificationProbe(t *testing.T, params any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal notification: %v", err)
	}
	return raw
}

func tokenUsageNotificationProbe(t *testing.T, threadID, turnID string, totalTokens int) json.RawMessage {
	t.Helper()
	return notificationProbe(t, map[string]any{
		"threadId": threadID,
		"turnId":   turnID,
		"tokenUsage": map[string]any{
			"total": map[string]any{},
			"last": map[string]any{
				"totalTokens": totalTokens,
			},
			"modelContextWindow": 258400,
		},
	})
}

func appServerCurrentTurn(s *appServerSession) string {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.currentTurn
}

func appServerPendingMessages(s *appServerSession) []string {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return append([]string(nil), s.pendingMsgs...)
}

func waitForWrittenJSONLine(t *testing.T, w *lockedWriteCloser) string {
	t.Helper()
	deadline := time.After(time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for JSON response, buffer=%q", w.String())
		case <-ticker.C:
			for _, line := range strings.Split(w.String(), "\n") {
				line = strings.TrimSpace(line)
				if line != "" {
					return line
				}
			}
		}
	}
}

func TestAppServerListenURL(t *testing.T) {
	cases := map[string]string{
		"":                     "",
		"  ":                   "",
		"stdio://":             "",
		"stdio":                "",
		"ws://127.0.0.1:3845":  "ws://127.0.0.1:3845",
		"ws://localhost:9000":  "ws://localhost:9000",
		"ws://127.0.0.1:3845 ": "ws://127.0.0.1:3845",
		" stdio ":              "",
		" stdio:// ":           "",
		"STDIO":                "",
		"STDIO://":             "",
	}
	for in, want := range cases {
		if got := appServerListenURL(in); got != want {
			t.Errorf("appServerListenURL(%q) = %q, want %q", in, got, want)
		}
	}
}
