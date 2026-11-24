package core

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type taskCardStubPlatform struct {
	stubPlatformEngine
	mu          sync.Mutex
	sends       []*Card
	updates     []*Card
	failSend    bool
	failUpdates int // number of upcoming UpdateTaskCard calls to fail
	updateCalls int
}

func (p *taskCardStubPlatform) SendTaskCard(_ context.Context, _ any, card *Card) (any, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failSend {
		return nil, context.DeadlineExceeded
	}
	p.sends = append(p.sends, card)
	return "handle-1", nil
}

func (p *taskCardStubPlatform) UpdateTaskCard(_ context.Context, _ any, card *Card) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.updateCalls++
	if p.failUpdates > 0 {
		p.failUpdates--
		return errors.New("feishu: update task card code=230429 msg=frequency limit")
	}
	p.updates = append(p.updates, card)
	return nil
}

func (p *taskCardStubPlatform) updateCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.updateCalls
}

func (p *taskCardStubPlatform) last() *Card {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n := len(p.updates); n > 0 {
		return p.updates[n-1]
	}
	if n := len(p.sends); n > 0 {
		return p.sends[n-1]
	}
	return nil
}

func cardBody(t *testing.T, card *Card) string {
	t.Helper()
	if card == nil || len(card.Elements) == 0 {
		t.Fatal("card has no elements")
	}
	md, ok := card.Elements[0].(CardMarkdown)
	if !ok {
		t.Fatalf("first element is %T, want CardMarkdown", card.Elements[0])
	}
	return md.Content
}

func hasStopButton(card *Card) bool {
	for _, el := range card.Elements {
		if actions, ok := el.(CardActions); ok && len(actions.Buttons) > 0 {
			return actions.Buttons[0].Value == "cmd:/stop"
		}
	}
	return false
}

func TestTurnCard_RendersRunningStateWithStopButton(t *testing.T) {
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangChinese)
	t.Cleanup(e.cancel)
	p := &taskCardStubPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}

	tc := newTurnCard(e, p, "ctx", "k")
	if tc == nil {
		t.Fatal("newTurnCard() = nil, want a card for a TaskCardSender platform")
	}
	tc.Start()
	// Simulate the first-render delay elapsing.
	tc.render(true)
	defer tc.Finish(taskCardDone, "")

	card := p.last()
	if card == nil || card.Header == nil {
		t.Fatal("no card sent on start")
	}
	if !strings.Contains(card.Header.Title, "正在处理") {
		t.Fatalf("header = %q, want running title", card.Header.Title)
	}
	if body := cardBody(t, card); !strings.Contains(body, "当前步骤") {
		t.Fatalf("body = %q, want current step line", body)
	}
	if !hasStopButton(card) {
		t.Fatal("running card must expose the stop button")
	}
}

func TestTurnCard_ReflectsStepQueueAndFinish(t *testing.T) {
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangChinese)
	t.Cleanup(e.cancel)
	p := &taskCardStubPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}

	tc := newTurnCard(e, p, "ctx", "k")
	tc.Start()
	tc.render(true)

	tc.SetStep("Bash · grep booking_check_fail_rate")
	tc.SetQueue([]string{"快点", "帮我看下 XX 订单"})
	// Throttled refreshes may be skipped within the 1.5s window; force one so
	// the assertions see the latest state.
	tc.render(true)

	card := p.last()
	body := cardBody(t, card)
	if !strings.Contains(body, "grep booking_check_fail_rate") {
		t.Fatalf("body = %q, want current step", body)
	}
	if !strings.Contains(body, "排队中 (2)") {
		t.Fatalf("body = %q, want queue header", body)
	}
	if !strings.Contains(body, "2. 帮我看下 XX 订单") {
		t.Fatalf("body = %q, want queued item", body)
	}

	tc.Finish(taskCardDone, "")
	final := p.last()
	if final.Header == nil || !strings.Contains(final.Header.Title, "已完成") {
		t.Fatalf("final header = %+v, want done title", final.Header)
	}
	if hasStopButton(final) {
		t.Fatal("finished card must not keep the stop button")
	}
}

func TestTurnCard_NilForUnsupportedPlatform(t *testing.T) {
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangChinese)
	t.Cleanup(e.cancel)
	if tc := newTurnCard(e, &stubPlatformEngine{n: "plain"}, "ctx", "k"); tc != nil {
		t.Fatal("newTurnCard() != nil for a platform without TaskCardSender")
	}
}

func TestFormatCardElapsed(t *testing.T) {
	cases := map[int]string{5: "0:05", 192: "3:12", 3730: "1:02:10"}
	for secs, want := range cases {
		if got := formatCardElapsed(time.Duration(secs) * time.Second); got != want {
			t.Errorf("formatCardElapsed(%ds) = %q, want %q", secs, got, want)
		}
	}
}

// A turn that finishes before the first render must not leave a card behind.
func TestTurnCard_QuickTurnLeavesNoCard(t *testing.T) {
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangChinese)
	t.Cleanup(e.cancel)
	p := &taskCardStubPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}

	tc := newTurnCard(e, p, "ctx", "k")
	tc.Start()
	tc.Finish(taskCardDone, "")

	if tc.Visible() {
		t.Fatal("card became visible for a turn that finished before the delay")
	}
	p.mu.Lock()
	sends := len(p.sends)
	p.mu.Unlock()
	if sends != 0 {
		t.Fatalf("sent %d cards for a quick turn, want 0", sends)
	}
}

// A follow-up that was queued behind a stopped turn must get its own live card.
func TestRotateTaskCardAfterStop(t *testing.T) {
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangChinese)
	t.Cleanup(e.cancel)
	p := &taskCardStubPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}

	old := newTurnCard(e, p, "ctx", "k")
	state := &interactiveState{taskCard: old, turnActive: true}
	old.Finish(taskCardStopped, "")

	fresh := e.rotateTaskCardIfFinished(state, p, "ctx2", "k")
	if fresh == nil {
		t.Fatal("rotateTaskCardIfFinished() = nil for a stopped card")
	}
	if fresh == old {
		t.Fatal("card was not rotated; the follow-up would run under a stopped card")
	}
	if fresh.Finished() {
		t.Fatal("fresh card is already finished")
	}

	running := e.rotateTaskCardIfFinished(state, p, "ctx3", "k")
	if running != fresh {
		t.Fatal("rotateTaskCardIfFinished() replaced a running card")
	}
}

// The card must appear only when the agent starts doing real work, so plain
// answers never leave an empty "准备中" card behind.
func TestTurnCardAppearsOnFirstToolUse(t *testing.T) {
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangChinese)
	t.Cleanup(e.cancel)
	p := &taskCardStubPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}

	tc := newTurnCard(e, p, "ctx", "k")
	tc.Start() // no-op
	if len(p.sends) != 0 {
		t.Fatalf("card sent before any work: %d", len(p.sends))
	}

	tc.EnsureStarted()
	if len(p.sends) != 1 {
		t.Fatalf("card sends = %d, want 1 after first tool use", len(p.sends))
	}
	tc.EnsureStarted() // idempotent
	if len(p.sends) != 1 {
		t.Fatalf("card re-sent on repeat EnsureStarted: %d", len(p.sends))
	}
	tc.Finish(taskCardDone, "")
}

// Card steps must be human-readable actions, never raw commands/paths/regex.
func TestTaskCardStepTextHumanized(t *testing.T) {
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangChinese)
	t.Cleanup(e.cancel)

	cases := []struct{ tool, want string }{
		{"Bash", "执行命令"},
		{"Read", "读取文件"},
		{"Edit", "修改文件"},
		{"Grep", "检索内容"},
		{"Glob", "查找文件"},
		{"WebSearch", "查询资料"},
		{"mcp__jinkela__query", "调用工具"},
	}
	for _, c := range cases {
		got := e.taskCardStepText(c.tool)
		if !strings.Contains(got, c.want) {
			t.Errorf("taskCardStepText(%q) = %q, want %q", c.tool, got, c.want)
		}
		if strings.Contains(got, "``") || strings.Contains(got, "/") {
			t.Errorf("taskCardStepText(%q) leaked internal detail: %q", c.tool, got)
		}
	}
}

func withFastTerminalRetries(t *testing.T) {
	t.Helper()
	old := taskCardTerminalRetryDelays
	taskCardTerminalRetryDelays = []time.Duration{0, 0, 0}
	t.Cleanup(func() { taskCardTerminalRetryDelays = old })
}

func TestTurnCard_FinishRetriesTerminalUpdate(t *testing.T) {
	withFastTerminalRetries(t)
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangChinese)
	t.Cleanup(e.cancel)
	p := &taskCardStubPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}, failUpdates: 1}

	tc := newTurnCard(e, p, "ctx", "k")
	tc.Start()
	tc.render(true)
	tc.Finish(taskCardDone, "")

	deadline := time.After(2 * time.Second)
	for {
		final := p.last()
		if final != nil && final.Header != nil && strings.Contains(final.Header.Title, "已完成") {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("final card = %+v, want a done title after retry", final)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	if got := p.updateCount(); got < 2 {
		t.Fatalf("UpdateTaskCard calls = %d, want at least 2 (one failure + one retry)", got)
	}
}

func TestTurnCard_FinishFallsBackToTextWhenUpdatesFail(t *testing.T) {
	withFastTerminalRetries(t)
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangChinese)
	t.Cleanup(e.cancel)
	p := &taskCardStubPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}, failUpdates: 100}

	tc := newTurnCard(e, p, "ctx", "k")
	tc.Start()
	tc.render(true)
	tc.Finish(taskCardDone, "")

	deadline := time.After(2 * time.Second)
	for {
		sent := p.getSent()
		if len(sent) > 0 && strings.Contains(sent[len(sent)-1], "已完成") {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("sent = %v, want a plain-text done fallback", sent)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestTurnCard_DegradedNoticeKeepsRetrying(t *testing.T) {
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangChinese)
	t.Cleanup(e.cancel)
	p := &taskCardStubPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}, failUpdates: 100}

	tc := newTurnCard(e, p, "ctx", "k")
	tc.Start()
	tc.render(true) // create the card (succeeds)
	for i := 0; i < taskCardDegradedThreshold; i++ {
		tc.render(true) // updates are rejected -> degraded notice, but not disabled
	}

	sent := p.getSent()
	if len(sent) == 0 || !strings.Contains(sent[len(sent)-1], "任务仍在继续") {
		t.Fatalf("sent = %v, want a degraded-card notice for the user", sent)
	}

	before := p.updateCount()
	tc.render(true) // force another attempt
	if got := p.updateCount(); got <= before {
		t.Fatalf("UpdateTaskCard calls = %d, want > %d (card must keep retrying)", got, before)
	}
}

// If the turn ends while the initial card POST is still in flight, Finish must
// still queue the terminal update; otherwise the card lands as "处理中" and the
// heartbeat is already stopped.
func TestTurnCard_FinishWhileCreateInFlight(t *testing.T) {
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangChinese)
	t.Cleanup(e.cancel)
	p := &taskCardStubPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}

	tc := newTurnCard(e, p, "ctx", "k")
	tc.Start()
	tc.mu.Lock()
	tc.creating = true // initial POST enqueued but not completed
	tc.mu.Unlock()

	tc.Finish(taskCardDone, "")

	final := p.last()
	if final == nil || final.Header == nil || !strings.Contains(final.Header.Title, "已完成") {
		t.Fatalf("final card = %+v, want a done terminal card", final)
	}
}

func withFastCardThrottle(t *testing.T) {
	t.Helper()
	old := taskCardMinRenderGap
	taskCardMinRenderGap = 0
	t.Cleanup(func() { taskCardMinRenderGap = old })
}

// The card must keep ticking during a long tool call with no agent events.
func TestTurnCard_TickerRefreshesWithoutEvents(t *testing.T) {
	withFastCardThrottle(t)
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangChinese)
	e.SetCardTickInterval(20 * time.Millisecond)
	t.Cleanup(e.cancel)
	p := &taskCardStubPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}

	tc := newTurnCard(e, p, "ctx", "k")
	tc.EnsureStarted()

	deadline := time.After(2 * time.Second)
	for {
		if p.updateCount() > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("card did not refresh without agent events")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	tc.Finish(taskCardDone, "")
}

// Finish must stop the ticker; no updates may continue after the terminal card.
func TestTurnCard_FinishStopsTicker(t *testing.T) {
	withFastCardThrottle(t)
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangChinese)
	e.SetCardTickInterval(20 * time.Millisecond)
	t.Cleanup(e.cancel)
	p := &taskCardStubPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}

	tc := newTurnCard(e, p, "ctx", "k")
	tc.EnsureStarted()

	deadline := time.After(2 * time.Second)
	for {
		if p.updateCount() > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("card did not refresh before Finish")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	tc.Finish(taskCardDone, "")
	// Wait for the terminal card to land.
	for {
		final := p.last()
		if final != nil && final.Header != nil && strings.Contains(final.Header.Title, "已完成") {
			break
		}
		select {
		case <-deadline:
			t.Fatal("terminal card did not land")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	before := p.updateCount()
	time.Sleep(80 * time.Millisecond)
	if got := p.updateCount(); got != before {
		t.Fatalf("ticker kept refreshing after Finish: %d -> %d", before, got)
	}
}

// A slow card PATCH must not queue unbounded refreshes behind it; a request
// arriving while one is in flight is coalesced into one pending render.
func TestTurnCard_RenderCoalescesWhileInFlight(t *testing.T) {
	withFastCardThrottle(t)
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangChinese)
	t.Cleanup(e.cancel)
	p := &taskCardStubPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}

	tc := newTurnCard(e, p, "ctx", "k")
	tc.render(true) // create the card

	tc.mu.Lock()
	tc.rendering = true // simulate an in-flight PATCH
	tc.mu.Unlock()
	before := p.updateCount()

	tc.render(true)
	tc.render(true)
	if got := p.updateCount(); got != before {
		t.Fatalf("UpdateTaskCard calls = %d, want %d (no new tasks while in flight)", got, before)
	}
	tc.mu.Lock()
	pending := tc.pendingRender
	tc.mu.Unlock()
	if !pending {
		t.Fatal("pendingRender = false, want a coalesced refresh")
	}
}

// While the agent is streaming output the ticker must stay quiet; it resumes
// after two quiet intervals.
func TestTurnCard_TickerQuietWhileAgentActive(t *testing.T) {
	withFastCardThrottle(t)
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangChinese)
	e.SetCardTickInterval(30 * time.Millisecond)
	t.Cleanup(e.cancel)
	p := &taskCardStubPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}

	tc := newTurnCard(e, p, "ctx", "k")
	tc.EnsureStarted()
	time.Sleep(10 * time.Millisecond) // let the create land
	tc.NoteActivity()

	before := p.updateCount()
	time.Sleep(45 * time.Millisecond) // one tick, but within the 60ms quiet window
	if got := p.updateCount(); got > before {
		t.Fatalf("card refreshed while the agent was active: %d -> %d", before, got)
	}

	deadline := time.After(500 * time.Millisecond)
	for {
		if p.updateCount() > before {
			return
		}
		select {
		case <-deadline:
			t.Fatal("card ticker did not resume after the quiet window")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}
