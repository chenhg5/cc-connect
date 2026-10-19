package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
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

func TestAppServerSession_FailRunningTurnEmitsOnlyWhenBusy(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 4)}
	s.failRunningTurn("idle")
	if len(s.events) != 0 {
		t.Fatalf("idle session emitted %d events", len(s.events))
	}
	s.currentTurn = "turn-1"
	s.failRunningTurn("exited mid-turn")
	if len(s.events) != 1 {
		t.Fatalf("busy session emitted %d events, want 1", len(s.events))
	}
	if ev := <-s.events; ev.Type != core.EventError || s.currentTurn != "" {
		t.Fatalf("event = %+v, currentTurn = %q", ev, s.currentTurn)
	}
}

func TestAppServerSession_ErrorNotificationSurfacesNestedMessage(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 4), currentTurn: "turn-1"}
	s.handleNotification("error", json.RawMessage(`{"error":{"message":"You've hit your usage limit. Try again at 4:13 AM.","codexErrorInfo":"usageLimitExceeded"},"willRetry":false,"threadId":"t","turnId":"turn-1"}`))
	// The failed turn/completed that follows must not emit a second event.
	s.handleNotification("turn/completed", json.RawMessage(`{"threadId":"t","turn":{"id":"turn-1","status":"failed","error":{"message":"You've hit your usage limit. Try again at 4:13 AM."}}}`))
	if len(s.events) != 1 {
		t.Fatalf("emitted %d events, want 1", len(s.events))
	}
	ev := <-s.events
	if ev.Type != core.EventError || ev.Error == nil || !strings.Contains(ev.Error.Error(), "usage limit") {
		t.Fatalf("event = %+v", ev)
	}
}

func TestAppServerSession_ErrorNotificationIgnoresRetry(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 4), currentTurn: "turn-1"}
	s.handleNotification("error", json.RawMessage(`{"error":{"message":"Reconnecting... 1/5"},"willRetry":true}`))
	if len(s.events) != 0 || s.currentTurn != "turn-1" {
		t.Fatalf("retryable error emitted %d events, currentTurn=%q", len(s.events), s.currentTurn)
	}
}

func TestAppServerSession_FailedTurnCompletedEmitsError(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 4), currentTurn: "turn-1"}
	s.handleNotification("turn/completed", json.RawMessage(`{"threadId":"t","turn":{"id":"turn-1","status":"failed","error":{"message":"usage limit"}}}`))
	if len(s.events) != 1 {
		t.Fatalf("emitted %d events, want 1", len(s.events))
	}
	if ev := <-s.events; ev.Type != core.EventError {
		t.Fatalf("event = %+v", ev)
	}
}

func TestAppServerSession_IgnoresOtherThreads(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 4), currentTurn: "turn-1"}
	s.threadID.Store("own")
	// codex's memory consolidation thread in the same process: its message and idle are not ours.
	s.handleNotification("item/completed", json.RawMessage(`{"threadId":"memory","turnId":"m1","item":{"type":"agentMessage","id":"x","text":"Raw memories/rollouts map to these durable groups"}}`))
	s.handleNotification("thread/status/changed", json.RawMessage(`{"threadId":"memory","status":{"type":"idle"}}`))
	if len(s.events) != 0 || s.currentTurn != "turn-1" {
		t.Fatalf("foreign thread leaked: %d events, currentTurn=%q", len(s.events), s.currentTurn)
	}
	// Our own thread still completes the turn.
	s.handleNotification("thread/status/changed", json.RawMessage(`{"threadId":"own","status":{"type":"idle"}}`))
	if s.currentTurn != "" {
		t.Fatalf("own idle did not complete the turn: currentTurn=%q", s.currentTurn)
	}
}

func TestAppServerSession_NoThreadYetPassesAll(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 4), currentTurn: "turn-1"}
	s.handleNotification("thread/status/changed", json.RawMessage(`{"threadId":"any","status":{"type":"idle"}}`))
	if s.currentTurn != "" {
		t.Fatalf("before our thread id is known nothing should be dropped: currentTurn=%q", s.currentTurn)
	}
}

// Notifications below are trimmed from a real codex 0.163.0-alpha.10.2 app-server
// run (2026-10-19): code mode exec cell -> exec_command (interactive python),
// write_stdin, empty write_stdin poll, view_image. Each must reach the engine as
// an event, or its idle watchdog kills a session that is busy in a long REPL.
func TestAppServerSession_CodeModeToolsEmitEvents(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 16), currentTurn: "turn-1"}
	s.threadID.Store("th")
	notes := []struct{ method, params string }{
		{"item/started", `{"threadId":"th","turnId":"turn-1","item":{"type":"commandExecution","id":"exec-1","command":"/bin/zsh -lc 'python3 -i -q'","processId":"22711","source":"unifiedExecStartup","status":"inProgress"}}`},
		{"item/commandExecution/terminalInteraction", `{"threadId":"th","turnId":"turn-1","itemId":"exec-1","processId":"22711","stdin":"print(6*7)\n"}`},
		{"item/commandExecution/terminalInteraction", `{"threadId":"th","turnId":"turn-1","itemId":"exec-1","processId":"22711","stdin":""}`},
		{"item/started", `{"threadId":"th","turnId":"turn-1","item":{"type":"imageView","id":"exec-2","path":"/private/tmp/cxcap/wd/a.png"}}`},
		{"item/completed", `{"threadId":"th","turnId":"turn-1","item":{"type":"imageView","id":"exec-2","path":"/private/tmp/cxcap/wd/a.png"}}`},
		{"item/completed", `{"threadId":"th","turnId":"turn-1","item":{"type":"commandExecution","id":"exec-1","command":"/bin/zsh -lc 'python3 -i -q'","processId":"22711","status":"completed","exitCode":0,"aggregatedOutput":"42\n"}}`},
		{"item/commandExecution/terminalInteraction", `{"threadId":"th","turnId":"turn-1","itemId":"exec-9","processId":"777","stdin":""}`},
	}
	for _, n := range notes {
		s.handleNotification(n.method, json.RawMessage(n.params))
	}
	want := []struct {
		typ   core.EventType
		tool  string
		input string
	}{
		{core.EventToolUse, "Bash", "/bin/zsh -lc 'python3 -i -q'"},
		{core.EventToolUse, "WriteStdin", "print(6*7)\n# -> /bin/zsh -lc 'python3 -i -q'"},
		{core.EventToolUse, "WriteStdin", "(poll) /bin/zsh -lc 'python3 -i -q'"},
		{core.EventToolUse, "ViewImage", "/private/tmp/cxcap/wd/a.png"},
		{core.EventToolResult, "ViewImage", ""},
		{core.EventToolResult, "Bash", "/bin/zsh -lc 'python3 -i -q'"},
		{core.EventToolUse, "WriteStdin", "(poll) process 777"},
	}
	if len(s.events) != len(want) {
		t.Fatalf("emitted %d events, want %d", len(s.events), len(want))
	}
	for i, w := range want {
		ev := <-s.events
		if ev.Type != w.typ || ev.ToolName != w.tool || ev.ToolInput != w.input {
			t.Fatalf("event %d = {%v %q %q}, want {%v %q %q}", i, ev.Type, ev.ToolName, ev.ToolInput, w.typ, w.tool, w.input)
		}
	}
	if len(s.execCommands) != 0 {
		t.Fatalf("completed command not forgotten: %v", s.execCommands)
	}
}

func TestAppServerSession_OtherToolItemsEmitEvents(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 16), currentTurn: "turn-1"}
	s.handleItemStarted(map[string]any{"type": "sleep", "id": "s1", "durationMs": float64(30000)})
	s.handleItemStarted(map[string]any{"type": "collabAgentToolCall", "id": "c1", "tool": "wait_agent", "prompt": ""})
	s.handleItemCompleted(map[string]any{"type": "collabAgentToolCall", "id": "c1", "tool": "wait_agent", "status": "completed"})
	s.handleItemStarted(map[string]any{"type": "imageGeneration", "id": "g1", "revisedPrompt": "a cat"})
	s.handleItemCompleted(map[string]any{"type": "imageGeneration", "id": "g1", "status": "completed", "savedPath": "/tmp/cat.png"})
	want := []struct {
		typ  core.EventType
		tool string
	}{
		{core.EventToolUse, "Sleep"},
		{core.EventToolUse, "Agent:wait_agent"},
		{core.EventToolResult, "Agent:wait_agent"},
		{core.EventToolUse, "ImageGen"},
		{core.EventToolResult, "ImageGen"},
	}
	if len(s.events) != len(want) {
		t.Fatalf("emitted %d events, want %d", len(s.events), len(want))
	}
	for i, w := range want {
		if ev := <-s.events; ev.Type != w.typ || ev.ToolName != w.tool {
			t.Fatalf("event %d = {%v %q}, want {%v %q}", i, ev.Type, ev.ToolName, w.typ, w.tool)
		}
	}
}
