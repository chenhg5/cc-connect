package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
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

func TestAppServerSession_InterruptSessionSendsTurnInterrupt(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()

	s := &appServerSession{
		ctx:        context.Background(),
		stdin:      writer,
		pending:    make(map[int64]chan rpcResponseEnvelope),
		interrupts: make(map[string][]chan error),
	}
	s.threadID.Store("thread-1")
	s.stateMu.Lock()
	s.currentTurn = "turn-1"
	s.stateMu.Unlock()

	done := make(chan error, 1)
	go func() {
		done <- s.InterruptSession(context.Background())
	}()

	scanner := bufio.NewScanner(reader)
	if !scanner.Scan() {
		t.Fatalf("expected interrupt request, scan err=%v", scanner.Err())
	}
	var payload struct {
		ID     int64          `json:"id"`
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &payload); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if payload.Method != "turn/interrupt" {
		t.Fatalf("method = %q, want turn/interrupt", payload.Method)
	}
	if payload.Params["threadId"] != "thread-1" || payload.Params["turnId"] != "turn-1" {
		t.Fatalf("params = %#v, want threadId and turnId", payload.Params)
	}

	s.handleResponse(rpcResponseEnvelope{ID: payload.ID, Result: json.RawMessage(`{}`)})
	notif := turnNotification{ThreadID: "thread-1"}
	notif.Turn.ID = "turn-1"
	notif.Turn.Status = "interrupted"
	s.completeTurnNotification(notif)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("InterruptSession returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("InterruptSession did not complete")
	}
}

func TestAppServerSession_InterruptSessionCoalescesConcurrentRequests(t *testing.T) {
	stdin := &recordingWriteCloser{}
	s := &appServerSession{
		ctx:        context.Background(),
		stdin:      stdin,
		pending:    make(map[int64]chan rpcResponseEnvelope),
		interrupts: make(map[string][]chan error),
	}
	s.threadID.Store("thread-1")
	s.stateMu.Lock()
	s.currentTurn = "turn-1"
	s.stateMu.Unlock()

	done1 := make(chan error, 1)
	done2 := make(chan error, 1)
	go func() { done1 <- s.InterruptSession(context.Background()) }()
	waitForAppServerCondition(t, time.Second, func() bool {
		return len(stdin.snapshot()) == 1
	})
	go func() { done2 <- s.InterruptSession(context.Background()) }()
	waitForAppServerCondition(t, time.Second, func() bool {
		s.stateMu.Lock()
		defer s.stateMu.Unlock()
		return len(s.interrupts["turn-1"]) == 2
	})

	lines := stdin.snapshot()
	if len(lines) != 1 {
		t.Fatalf("interrupt request count = %d, want 1", len(lines))
	}
	var payload struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &payload); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	s.handleResponse(rpcResponseEnvelope{ID: payload.ID, Result: json.RawMessage(`{}`)})
	notif := turnNotification{ThreadID: "thread-1"}
	notif.Turn.ID = "turn-1"
	notif.Turn.Status = "interrupted"
	s.completeTurnNotification(notif)

	for i, ch := range []chan error{done1, done2} {
		select {
		case err := <-ch:
			if err != nil {
				t.Fatalf("interrupt %d returned error: %v", i, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("interrupt %d did not complete", i)
		}
	}
}

func TestAppServerSession_InterruptSessionContextTimeoutCleansPending(t *testing.T) {
	stdin := &recordingWriteCloser{}
	s := &appServerSession{
		ctx:        context.Background(),
		stdin:      stdin,
		pending:    make(map[int64]chan rpcResponseEnvelope),
		interrupts: make(map[string][]chan error),
	}
	s.threadID.Store("thread-1")
	s.stateMu.Lock()
	s.currentTurn = "turn-1"
	s.stateMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err := s.InterruptSession(ctx)
	if err == nil {
		t.Fatal("InterruptSession returned nil, want timeout")
	}

	s.pendingMu.Lock()
	pendingLen := len(s.pending)
	s.pendingMu.Unlock()
	if pendingLen != 0 {
		t.Fatalf("pending len = %d, want 0", pendingLen)
	}
	s.stateMu.Lock()
	interruptLen := len(s.interrupts)
	s.stateMu.Unlock()
	if interruptLen != 0 {
		t.Fatalf("interrupts len = %d, want 0", interruptLen)
	}
}

func TestAppServerSession_CompleteTurnNotificationSignalsAllWaiters(t *testing.T) {
	ch1 := make(chan error, 1)
	ch2 := make(chan error, 1)
	s := &appServerSession{
		interrupts: map[string][]chan error{
			"turn-1": {ch1, ch2},
		},
	}

	notif := turnNotification{ThreadID: "thread-1"}
	notif.Turn.ID = "turn-1"
	notif.Turn.Status = "interrupted"
	s.completeTurnNotification(notif)

	for i, ch := range []chan error{ch1, ch2} {
		select {
		case err := <-ch:
			if err != nil {
				t.Fatalf("waiter %d error = %v, want nil", i, err)
			}
		default:
			t.Fatalf("waiter %d was not signaled", i)
		}
	}
	if got := len(s.interrupts); got != 0 {
		t.Fatalf("interrupts len = %d, want 0", got)
	}
}

func TestAppServerSession_TurnCompletedAtomicallyClearsCurrentTurnAndWaiters(t *testing.T) {
	ch := make(chan error, 1)
	s := &appServerSession{
		events:     make(chan core.Event, 4),
		interrupts: map[string][]chan error{"turn-1": {ch}},
	}
	s.threadID.Store("thread-1")
	s.stateMu.Lock()
	s.currentTurn = "turn-1"
	s.stateMu.Unlock()

	notif := turnNotification{ThreadID: "thread-1"}
	notif.Turn.ID = "turn-1"
	notif.Turn.Status = "interrupted"
	raw, err := json.Marshal(notif)
	if err != nil {
		t.Fatalf("marshal notification: %v", err)
	}

	s.handleNotification("turn/completed", raw)

	select {
	case err := <-ch:
		if err != nil {
			t.Fatalf("interrupt waiter error = %v, want nil", err)
		}
	default:
		t.Fatal("interrupt waiter was not signaled")
	}
	s.stateMu.Lock()
	currentTurn := s.currentTurn
	interruptLen := len(s.interrupts)
	s.stateMu.Unlock()
	if currentTurn != "" {
		t.Fatalf("currentTurn = %q, want empty", currentTurn)
	}
	if interruptLen != 0 {
		t.Fatalf("interrupts len = %d, want 0", interruptLen)
	}
	if err := s.InterruptSession(context.Background()); err == nil {
		t.Fatal("InterruptSession returned nil after turn completion, want no active turn")
	}
}

func TestAppServerSession_RejectInterruptsSignalsWaiters(t *testing.T) {
	ch := make(chan error, 1)
	s := &appServerSession{
		interrupts: map[string][]chan error{"turn-1": {ch}},
	}
	want := io.EOF

	s.rejectInterrupts(want)

	select {
	case err := <-ch:
		if err != want {
			t.Fatalf("reject error = %v, want %v", err, want)
		}
	default:
		t.Fatal("waiter was not signaled")
	}
	if got := len(s.interrupts); got != 0 {
		t.Fatalf("interrupts len = %d, want 0", got)
	}
}

func TestAppServerSession_RejectInterruptTurnSignalsOnlyMatchingTurn(t *testing.T) {
	ch1 := make(chan error, 1)
	ch2 := make(chan error, 1)
	s := &appServerSession{
		interrupts: map[string][]chan error{
			"turn-1": {ch1},
			"turn-2": {ch2},
		},
	}
	want := io.ErrClosedPipe

	s.rejectInterruptTurn("turn-1", want)

	select {
	case err := <-ch1:
		if err != want {
			t.Fatalf("reject error = %v, want %v", err, want)
		}
	default:
		t.Fatal("matching waiter was not signaled")
	}
	select {
	case err := <-ch2:
		t.Fatalf("non-matching waiter got error %v", err)
	default:
	}
	if _, ok := s.interrupts["turn-1"]; ok {
		t.Fatal("turn-1 waiters were not cleared")
	}
	if _, ok := s.interrupts["turn-2"]; !ok {
		t.Fatal("turn-2 waiters should remain")
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

var _ interface {
	GetUsage(context.Context) (*core.UsageReport, error)
} = (*appServerSession)(nil)

var _ interface {
	GetContextUsage() *core.ContextUsage
} = (*appServerSession)(nil)

var _ core.SessionInterrupter = (*appServerSession)(nil)

type recordingWriteCloser struct {
	mu    sync.Mutex
	lines []string
}

func (w *recordingWriteCloser) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lines = append(w.lines, string(append([]byte(nil), p...)))
	return len(p), nil
}

func (w *recordingWriteCloser) Close() error { return nil }

func (w *recordingWriteCloser) snapshot() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	cp := make([]string, len(w.lines))
	copy(cp, w.lines)
	return cp
}

func waitForAppServerCondition(t *testing.T, timeout time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}
