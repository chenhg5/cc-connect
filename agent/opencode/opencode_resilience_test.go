package opencode

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// TestHandleText_CompactionSummarySuppressed verifies that OpenCode's
// automatic compaction summary (plain text starting with "## Objective" and
// containing "## Important Details") is suppressed instead of being forwarded
// as a user-visible EventText. Regression: compaction summaries used to leak
// into the chat as giant formatted dumps and end the turn prematurely.
func TestHandleText_CompactionSummarySuppressed(t *testing.T) {
	summary := "## Objective\n- 主线：迁移 Nacos\n\n## Important Details\n- 硬约束：不允许装 helm\n\n## Work State\n- 已完成 ACR tag"
	jsonData, _ := json.Marshal(map[string]any{
		"type": "text",
		"part": map[string]any{"type": "text", "text": summary},
	})

	var raw map[string]any
	if err := json.Unmarshal(jsonData, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &opencodeSession{events: make(chan core.Event, 1), ctx: ctx}
	s.handleText(raw)

	select {
	case evt := <-s.events:
		t.Errorf("compaction summary leaked as EventText: %+v", evt)
	default:
		// expected: suppressed
	}
	if !s.expectingContinue.Load() {
		t.Error("expectingContinue not set after compaction summary suppressed")
	}
}

// TestHandleText_NormalTextForwarded verifies regular assistant text is still
// forwarded after the compaction-summary suppression is in place.
func TestHandleText_NormalTextForwarded(t *testing.T) {
	jsonData, _ := json.Marshal(map[string]any{
		"type": "text",
		"part": map[string]any{"type": "text", "text": "测试 56 全绿，dist 已重建。"},
	})

	var raw map[string]any
	if err := json.Unmarshal(jsonData, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &opencodeSession{events: make(chan core.Event, 2), ctx: ctx}

	// Text is buffered per step; nothing is forwarded until step_finish.
	s.handleText(raw)
	select {
	case evt := <-s.events:
		t.Errorf("text forwarded before step_finish: %+v", evt)
	default:
		// expected: buffered
	}
	if s.expectingContinue.Load() {
		t.Error("expectingContinue set for normal text")
	}

	// An intermediate step (finish reason "tool-calls") forwards the buffered
	// text as a normal EventText (the engine folds it into the thinking panel
	// on streaming-card platforms).
	finishData, _ := json.Marshal(map[string]any{
		"type": "step-finish",
		"part": map[string]any{"type": "step-finish", "reason": "tool-calls"},
	})
	var finishRaw map[string]any
	if err := json.Unmarshal(finishData, &finishRaw); err != nil {
		t.Fatalf("unmarshal step-finish: %v", err)
	}
	s.handleStepFinish(finishRaw)

	select {
	case evt := <-s.events:
		if evt.Type != core.EventText {
			t.Errorf("event type = %q, want EventText", evt.Type)
		}
		if !strings.Contains(evt.Content, "测试 56 全绿") {
			t.Errorf("event content = %q, want normal text", evt.Content)
		}
	default:
		t.Error("expected buffered text forwarded at step_finish")
	}
}

// TestIsOpencodeCompactionSummary covers the recognizer boundaries: the
// summary template is detected, while ordinary replies are not.
func TestIsOpencodeCompactionSummary(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"full summary", "## Objective\n- x\n\n## Important Details\n- y\n\n## Work State\n- z", true},
		{"objective only", "## Objective\n- x", true},
		{"objective + work state", "## Objective\n- x\n\n## Work State\n- z", true},
		{"not summary - plain reply", "测试完成，全部通过。", false},
		{"not summary - mentions objective inline", "我完成了目标（objective）：迁移完成", false},
		{"not summary - empty", "", false},
		{"not summary - heading in middle", "先看结果\n\n## Objective\n- x", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isOpencodeCompactionSummary(c.text); got != c.want {
				t.Errorf("isOpencodeCompactionSummary(%q) = %v, want %v", c.text, got, c.want)
			}
		})
	}
}

// TestHandleError_SendsEventError verifies the EventError carries the
// extracted message. (ErrorKind classification is intentionally omitted until
// PR #1641 fix/codex-retry merges upstream.)
func TestHandleError_SendsEventError(t *testing.T) {
	jsonData, _ := json.Marshal(map[string]any{
		"type": "error",
		"error": map[string]any{
			"name":    "UnknownError",
			"message": "Unexpected server error. Check server logs for details.",
		},
	})

	var raw map[string]any
	if err := json.Unmarshal(jsonData, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &opencodeSession{events: make(chan core.Event, 1), ctx: ctx}
	s.handleError(raw)

	select {
	case evt := <-s.events:
		if evt.Type != core.EventError {
			t.Errorf("event type = %q, want EventError", evt.Type)
		}
		if !strings.Contains(evt.Error.Error(), "Unexpected server error") {
			t.Errorf("error = %q, want message about unexpected server error", evt.Error)
		}
	default:
		t.Error("expected EventError to be sent")
	}
}

// TestIsCompactionReasoning covers the compaction-reasoning recognizer: the
// characteristic compaction thought prefixes are detected, ordinary reasoning
// is not.
func TestIsCompactionReasoning(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"canonical prefix", "Let me analyze the conversation to build a comprehensive summary that combines the prior summary with the new conversation.", true},
		{"combine prior summary", "Let me combine the prior summary with the conversation to produce a new, updated summary.", true},
		{"case insensitive", "let me ANALYZE the conversation to build a comprehensive summary", true},
		{"normal reasoning", "继续排查镜像 tag 不一致问题。先从 chart 提取全部镜像名。", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isCompactionReasoning(c.text); got != c.want {
				t.Errorf("isCompactionReasoning(%q) = %v, want %v", c.text, got, c.want)
			}
		})
	}
}

// TestHandleReasoning_CompactionReasoningSuppressed verifies compaction
// reasoning does not leak as an EventThinking to the engine.
func TestHandleReasoning_CompactionReasoningSuppressed(t *testing.T) {
	jsonData, _ := json.Marshal(map[string]any{
		"type": "reasoning",
		"part": map[string]any{"type": "reasoning", "text": "Let me analyze the conversation to build a comprehensive summary that combines the prior summary with the new conversation. Key events in the conversation: ..."},
	})

	var raw map[string]any
	if err := json.Unmarshal(jsonData, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &opencodeSession{events: make(chan core.Event, 1), ctx: ctx}
	s.handleReasoning(raw)

	select {
	case evt := <-s.events:
		t.Errorf("compaction reasoning leaked as EventThinking: %+v", evt)
	default:
		// expected: suppressed
	}
}

// TestHandleReasoning_NormalReasoningForwarded verifies normal reasoning still
// reaches the engine.
func TestHandleReasoning_NormalReasoningForwarded(t *testing.T) {
	jsonData, _ := json.Marshal(map[string]any{
		"type": "reasoning",
		"part": map[string]any{"type": "reasoning", "text": "继续排查镜像 tag 问题。"},
	})

	var raw map[string]any
	if err := json.Unmarshal(jsonData, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &opencodeSession{events: make(chan core.Event, 1), ctx: ctx}
	s.handleReasoning(raw)

	select {
	case evt := <-s.events:
		if evt.Type != core.EventThinking {
			t.Errorf("event type = %q, want EventThinking", evt.Type)
		}
	default:
		t.Error("expected normal reasoning to be forwarded")
	}
}

// TestSendEventResult_DeferredAfterCompaction verifies the EventResult is not
// emitted while expectingContinue is set (compaction pending): the turn must
// wait for the automatic "continue" resume instead of ending with an empty
// reply.
func TestSendEventResult_DeferredAfterCompaction(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &opencodeSession{events: make(chan core.Event, 2), ctx: ctx}
	s.expectingContinue.Store(true)

	s.sendEventResult("")

	select {
	case evt := <-s.events:
		t.Errorf("EventResult emitted despite pending compaction: %+v", evt)
	default:
		// expected: deferred
	}
	if s.resultSent.Load() {
		t.Error("resultSent set despite deferred EventResult")
	}

	// After the compaction is resolved (continue process running), the next
	// sendEventResult must emit.
	s.expectingContinue.Store(false)
	s.sendEventResult("")
	select {
	case evt := <-s.events:
		if evt.Type != core.EventResult {
			t.Errorf("event type = %q, want EventResult", evt.Type)
		}
	default:
		t.Error("expected EventResult after compaction resolved")
	}
}

// TestHandleStepFinish_FinalStepCarriesAnswer verifies that the final step
// (finish reason "stop") does NOT forward its text as EventText — it is
// delivered once via EventResult.Content, so the final reply contains ONLY
// the answer, not the accumulated per-step narration.
func TestHandleStepFinish_FinalStepCarriesAnswer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &opencodeSession{events: make(chan core.Event, 3), ctx: ctx}

	answer := "都已处理。代码提交 0a66f11…6a74b07…"
	textData, _ := json.Marshal(map[string]any{
		"type": "text",
		"part": map[string]any{"type": "text", "text": answer},
	})
	var textRaw map[string]any
	if err := json.Unmarshal(textData, &textRaw); err != nil {
		t.Fatalf("unmarshal text: %v", err)
	}
	s.handleText(textRaw)

	finishData, _ := json.Marshal(map[string]any{
		"type": "step-finish",
		"part": map[string]any{"type": "step-finish", "reason": "stop"},
	})
	var finishRaw map[string]any
	if err := json.Unmarshal(finishData, &finishRaw); err != nil {
		t.Fatalf("unmarshal step-finish: %v", err)
	}
	s.handleStepFinish(finishRaw)

	// The answer is NOT forwarded as EventText — the first event must be the
	// EventResult itself. If an EventText leaks through (regression), the
	// first event would be an EventText and the assertion fails.
	select {
	case evt := <-s.events:
		if evt.Type != core.EventResult {
			t.Fatalf("first event = %+v, want EventResult (final step must not forward EventText)", evt)
		}
		if evt.Content != answer {
			t.Errorf("EventResult.Content = %q, want %q", evt.Content, answer)
		}
		if !evt.Done {
			t.Error("EventResult.Done must be true")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for EventResult")
	}

	// Nothing else follows the EventResult.
	select {
	case evt := <-s.events:
		t.Errorf("unexpected extra event after EventResult: %+v", evt)
	default:
		// expected: no extra events
	}
}

// TestHandleStepFinish_TokenUsageAccumulated verifies that OpenCode's
// per-step "tokens" field is parsed, accumulated across the turn, carried on
// EventResult (so engine logs show real input/output/cache numbers instead
// of 0) and exposed via ContextUsageReporter for the reply footer.
func TestHandleStepFinish_TokenUsageAccumulated(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &opencodeSession{events: make(chan core.Event, 4), ctx: ctx}

	// Two intermediate steps + one final step; tokens accumulate.
	intermediate := []map[string]any{
		{"type": "step-finish", "part": map[string]any{
			"type": "step-finish", "reason": "tool-calls",
			"tokens": map[string]any{"total": 10000, "input": 800, "output": 120, "reasoning": 40,
				"cache": map[string]any{"write": 0, "read": 9000}}}},
		{"type": "step-finish", "part": map[string]any{
			"type": "step-finish", "reason": "tool-calls",
			"tokens": map[string]any{"total": 12000, "input": 300, "output": 60, "reasoning": 20,
				"cache": map[string]any{"write": 500, "read": 11000}}}},
	}
	for _, raw := range intermediate {
		b, _ := json.Marshal(raw)
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		s.handleStepFinish(m)
	}

	final := map[string]any{"type": "step-finish", "part": map[string]any{
		"type": "step-finish", "reason": "stop",
		"tokens": map[string]any{"total": 259894, "input": 631, "output": 240, "reasoning": 79,
			"cache": map[string]any{"write": 0, "read": 258944}}}}
	b, _ := json.Marshal(final)
	var finalRaw map[string]any
	if err := json.Unmarshal(b, &finalRaw); err != nil {
		t.Fatalf("unmarshal final: %v", err)
	}
	s.handleStepFinish(finalRaw)

	var evt core.Event
	select {
	case evt = <-s.events:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for EventResult")
	}
	if evt.Type != core.EventResult {
		t.Fatalf("event = %+v, want EventResult", evt)
	}
	if evt.InputTokens != 800+300+631 {
		t.Errorf("InputTokens = %d, want %d", evt.InputTokens, 800+300+631)
	}
	if evt.OutputTokens != 120+60+240 {
		t.Errorf("OutputTokens = %d, want %d", evt.OutputTokens, 120+60+240)
	}
	if evt.CacheReadInputTokens != 9000+11000+258944 {
		t.Errorf("CacheReadInputTokens = %d, want %d", evt.CacheReadInputTokens, 9000+11000+258944)
	}
	if evt.CacheCreationInputTokens != 0+500+0 {
		t.Errorf("CacheCreationInputTokens = %d, want %d", evt.CacheCreationInputTokens, 0+500+0)
	}

	// ContextUsageReporter exposes the same turn totals (total = latest
	// snapshot of the context load).
	usage := s.GetContextUsage()
	if usage == nil {
		t.Fatal("GetContextUsage() = nil, want usage")
	}
	if usage.TotalTokens != 259894 {
		t.Errorf("TotalTokens = %d, want latest snapshot 259894", usage.TotalTokens)
	}
	if usage.InputTokens != evt.InputTokens || usage.OutputTokens != evt.OutputTokens {
		t.Errorf("GetContextUsage turn totals mismatch: in=%d/%d out=%d/%d",
			usage.InputTokens, evt.InputTokens, usage.OutputTokens, evt.OutputTokens)
	}
}

func TestStallWatchdog_DoesNotKillWhileToolRuns(t *testing.T) {
	s := &opencodeSession{}
	if s.stalledTooLong(defaultStallTimeout - time.Second) {
		t.Fatal("idle below the threshold must not be a stall")
	}
	if !s.stalledTooLong(defaultStallTimeout + time.Minute) {
		t.Fatal("idle above the threshold with no tool running must be a stall")
	}
	s.activeTools.Store(1)
	if s.stalledTooLong(defaultStallTimeout + time.Hour) {
		t.Fatal("a long-running tool call is work, not a stall")
	}
	s.activeTools.Store(0)
	if !s.stalledTooLong(defaultStallTimeout + time.Minute) {
		t.Fatal("after the tool finishes the watchdog must be armed again")
	}
}

func TestStallWatchdog_ConfigurableTimeout(t *testing.T) {
	s := &opencodeSession{stallTimeout: 30 * time.Minute}
	if s.stalledTooLong(defaultStallTimeout + time.Minute) {
		t.Fatal("a raised threshold must not keep the 5m default")
	}
	if !s.stalledTooLong(30*time.Minute + time.Second) {
		t.Fatal("a raised threshold must still arm at its own value")
	}

	off := &opencodeSession{stallTimeout: stallTimeoutDisabled}
	if off.stalledTooLong(24 * time.Hour) {
		t.Fatal("opencode_stall_timeout=off must disable the watchdog")
	}
}

func TestParseStallTimeout(t *testing.T) {
	cases := []struct {
		raw     any
		want    time.Duration
		wantErr bool
	}{
		{raw: nil, want: 0},
		{raw: "", want: 0},
		{raw: " 10m ", want: 10 * time.Minute},
		{raw: "90s", want: 90 * time.Second},
		{raw: "off", want: stallTimeoutDisabled},
		{raw: "OFF", want: stallTimeoutDisabled},
		{raw: "0", want: stallTimeoutDisabled},
		{raw: "none", want: stallTimeoutDisabled},
		{raw: "disabled", want: stallTimeoutDisabled},
		{raw: "10", wantErr: true},
		{raw: "-5m", wantErr: true},
		{raw: "fast", wantErr: true},
		{raw: 600, wantErr: true},
		{raw: true, wantErr: true},
	}
	for _, tc := range cases {
		got, err := parseStallTimeout(tc.raw)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("parseStallTimeout(%v) error = nil, want error", tc.raw)
			}
			continue
		}
		if err != nil {
			t.Fatalf("parseStallTimeout(%v): %v", tc.raw, err)
		}
		if got != tc.want {
			t.Fatalf("parseStallTimeout(%v) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestNew_StallTimeoutOption(t *testing.T) {
	truePath := func(t *testing.T) string {
		t.Helper()
		for _, p := range []string{"/usr/bin/true", "/bin/true"} {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
		t.Skip("no true binary available")
		return ""
	}
	a, err := New(map[string]any{"cmd": truePath(t), "opencode_stall_timeout": "45m"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	oc, ok := a.(*Agent)
	if !ok {
		t.Fatalf("New = %T, want *Agent", a)
	}
	if got := oc.stallTimeout; got != 45*time.Minute {
		t.Fatalf("stallTimeout = %v, want 45m", got)
	}

	a, err = New(map[string]any{"cmd": truePath(t)})
	if err != nil {
		t.Fatalf("New without option: %v", err)
	}
	if got := a.(*Agent).stallTimeout; got != 0 {
		t.Fatalf("default stallTimeout = %v, want 0 (5m default)", got)
	}

	if _, err := New(map[string]any{"cmd": truePath(t), "opencode_stall_timeout": "soon"}); err != nil {
		t.Fatalf("New with an unparsable option must fall back to the default, got %v", err)
	}

	for _, raw := range []any{"soon", 600, ""} {
		a, err := New(map[string]any{"cmd": truePath(t), "opencode_stall_timeout": raw})
		if err != nil {
			t.Fatalf("New with opencode_stall_timeout=%v: %v", raw, err)
		}
		if got := a.(*Agent).stallTimeout; got != 0 {
			t.Fatalf("stallTimeout for opencode_stall_timeout=%v = %v, want 0 (5m default)", raw, got)
		}
	}
}

func TestHandleToolUse_TracksInFlightTools(t *testing.T) {
	s := &opencodeSession{events: make(chan core.Event, 8), ctx: context.Background()}
	s.handleToolUse(map[string]any{"part": map[string]any{
		"tool":  "bash",
		"state": map[string]any{"status": "running", "input": "npm install"},
	}})
	if got := s.activeTools.Load(); got != 1 {
		t.Fatalf("activeTools after tool start = %d, want 1", got)
	}
	s.handleToolUse(map[string]any{"part": map[string]any{
		"tool":  "bash",
		"state": map[string]any{"status": "completed", "output": "done"},
	}})
	if got := s.activeTools.Load(); got != 0 {
		t.Fatalf("activeTools after tool completion = %d, want 0", got)
	}
	s.handleToolUse(map[string]any{"part": map[string]any{
		"tool":  "bash",
		"state": map[string]any{"status": "error", "error": "denied"},
	}})
	if got := s.activeTools.Load(); got != 0 {
		t.Fatalf("activeTools after tool error = %d, want 0 (must not go negative)", got)
	}
	s.handleToolUse(map[string]any{"part": map[string]any{"tool": "bash"}})
	if got := s.activeTools.Load(); got != 0 {
		t.Fatalf("activeTools without a status = %d, want 0 (unknown status must not arm)", got)
	}
}
