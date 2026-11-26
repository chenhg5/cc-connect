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

func (p *taskCardStubPlatform) sendCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.sends)
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
	if !strings.Contains(body, "**当前步骤：**`Bash · grep booking_check_fail_rate`") {
		t.Fatalf("body = %q, want inline current step", body)
	}
	if !strings.Contains(body, "排队中 (2)") {
		t.Fatalf("body = %q, want queue header", body)
	}
	if !strings.Contains(body, "↳ 2. `帮我看下 XX 订单`") {
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

// A turn that finishes within the start delay must not leave a card behind.
func TestTurnCard_QuickTurnLeavesNoCard(t *testing.T) {
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangChinese)
	e.SetCardStartDelay(100 * time.Millisecond)
	t.Cleanup(e.cancel)
	p := &taskCardStubPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}

	tc := newTurnCard(e, p, "ctx", "k")
	tc.Start()
	tc.Finish(taskCardDone, "")
	// The timer would have fired by now if Finish had not cancelled it.
	time.Sleep(150 * time.Millisecond)

	if tc.Visible() {
		t.Fatal("card became visible for a turn that finished before the delay")
	}
	if sends := p.sendCount(); sends != 0 {
		t.Fatalf("sent %d cards for a quick turn, want 0", sends)
	}
}

// A long turn gets the card at the threshold even without any tool or model
// event; step/queue updates recorded before the threshold land in that first
// post.
func TestTurnCard_DelayedStartPostsCardAfterThreshold(t *testing.T) {
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangChinese)
	e.SetCardStartDelay(50 * time.Millisecond)
	t.Cleanup(e.cancel)
	p := &taskCardStubPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}

	tc := newTurnCard(e, p, "ctx", "k")
	tc.SetStep("Bash · git status")
	tc.SetQueue([]string{"帮我查下订单"})
	start := time.Now()
	tc.Start()

	deadline := time.After(2 * time.Second)
	for p.last() == nil {
		select {
		case <-deadline:
			t.Fatal("card was not posted after the start delay")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Fatalf("card posted after %v, want at least the 50ms delay", elapsed)
	}
	body := cardBody(t, p.last())
	if !strings.Contains(body, "**当前步骤：**`Bash · git status`") {
		t.Fatalf("body = %q, want the pre-threshold step in the first post", body)
	}
	if !strings.Contains(body, "↳ 1. `帮我查下订单`") {
		t.Fatalf("body = %q, want the pre-threshold queue in the first post", body)
	}
	tc.Finish(taskCardDone, "")
}

// Step and queue updates before the threshold only touch memory: they must not
// create the card on their own.
func TestTurnCard_UpdatesBeforeStartStayInMemory(t *testing.T) {
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangChinese)
	e.SetCardStartDelay(5 * time.Second)
	t.Cleanup(e.cancel)
	p := &taskCardStubPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}

	tc := newTurnCard(e, p, "ctx", "k")
	tc.Start()
	tc.SetStep("Bash · ls")
	tc.SetQueue([]string{"排队消息"})
	time.Sleep(20 * time.Millisecond)
	if p.last() != nil {
		t.Fatal("step/queue update posted the card before the start threshold")
	}

	// A forced start (manual EnsureStarted here) posts the accumulated state.
	tc.EnsureStarted()
	deadline := time.After(2 * time.Second)
	for p.last() == nil {
		select {
		case <-deadline:
			t.Fatal("card did not post after EnsureStarted")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	body := cardBody(t, p.last())
	if !strings.Contains(body, "**当前步骤：**`Bash · ls`") || !strings.Contains(body, "↳ 1. `排队消息`") {
		t.Fatalf("body = %q, want the accumulated step and queue", body)
	}
	tc.Finish(taskCardDone, "")
}

// delay 0 keeps the old immediate behavior for deployments that want a card on
// every turn.
func TestTurnCard_ZeroDelayStartsImmediately(t *testing.T) {
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangChinese)
	e.SetCardStartDelay(0)
	t.Cleanup(e.cancel)
	p := &taskCardStubPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}

	tc := newTurnCard(e, p, "ctx", "k")
	tc.Start()
	deadline := time.After(2 * time.Second)
	for p.last() == nil {
		select {
		case <-deadline:
			t.Fatal("card did not post immediately with delay 0")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	tc.Finish(taskCardDone, "")
}

// A final result delivered before the threshold must not be followed by a
// card that appears during slow post-reply work.
func TestTurnCard_AbortStartPreventsCardAfterResult(t *testing.T) {
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangChinese)
	e.SetCardStartDelay(30 * time.Millisecond)
	t.Cleanup(e.cancel)
	p := &taskCardStubPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}

	tc := newTurnCard(e, p, "ctx", "k")
	tc.Start()
	tc.AbortStart()
	time.Sleep(60 * time.Millisecond)
	if sends := p.sendCount(); sends != 0 {
		t.Fatalf("card sent %d times after the turn's result landed", sends)
	}
	tc.Finish(taskCardDone, "")
}

// A queued turn reusing the card after an abort gets a fresh threshold.
func TestTurnCard_RearmStartAfterAbort(t *testing.T) {
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangChinese)
	e.SetCardStartDelay(30 * time.Millisecond)
	t.Cleanup(e.cancel)
	p := &taskCardStubPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}

	tc := newTurnCard(e, p, "ctx", "k")
	tc.Start()
	tc.AbortStart()
	tc.RearmStart()

	deadline := time.After(2 * time.Second)
	for p.last() == nil {
		select {
		case <-deadline:
			t.Fatal("card was not posted after re-arming the delayed start")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	if sends := p.sendCount(); sends != 1 {
		t.Fatalf("card sends = %d, want exactly 1 after re-arm", sends)
	}
	tc.Finish(taskCardDone, "")
}

// A queued entry that already started running must disappear from the queue
// mirror before the first card is posted.
func TestSyncTaskCardQueueClearsPoppedEntryBeforeStart(t *testing.T) {
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangChinese)
	e.SetCardStartDelay(200 * time.Millisecond)
	t.Cleanup(e.cancel)
	p := &taskCardStubPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}

	tc := newTurnCard(e, p, "ctx", "k")
	state := &interactiveState{taskCard: tc, pendingMessages: []queuedMessage{{content: "second message"}}}
	e.syncTaskCardQueue(state)

	// The drain pops the entry and starts it as the next turn.
	state.mu.Lock()
	state.pendingMessages = nil
	state.mu.Unlock()
	e.syncTaskCardQueue(state)

	tc.mu.Lock()
	queued := len(tc.queue)
	tc.mu.Unlock()
	if queued != 0 {
		t.Fatalf("queue mirror = %d entries, want 0 after the entry started running", queued)
	}

	tc.EnsureStarted()
	body := cardBody(t, p.last())
	if strings.Contains(body, "排队中") {
		t.Fatalf("body = %q, a running entry must not appear as queued", body)
	}
	tc.Finish(taskCardDone, "")
}

// Repeated Start calls arm the delay timer once: one scheduled log, one
// goroutine. EnsureStarted remains the single creator either way.
func TestTurnCard_StartTwiceSchedulesOnce(t *testing.T) {
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangChinese)
	e.SetCardStartDelay(30 * time.Millisecond)
	t.Cleanup(e.cancel)
	p := &taskCardStubPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}

	tc := newTurnCard(e, p, "ctx", "k")
	tc.Start()
	tc.Start()
	tc.mu.Lock()
	scheduled := tc.startScheduled
	tc.mu.Unlock()
	if !scheduled {
		t.Fatal("startScheduled = false after Start")
	}

	deadline := time.After(2 * time.Second)
	for p.last() == nil {
		select {
		case <-deadline:
			t.Fatal("card was not posted")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	if got := p.sendCount(); got != 1 {
		t.Fatalf("card sends = %d, want 1", got)
	}
	tc.Finish(taskCardDone, "")
}

// The threshold and the elapsed clock are anchored to when the turn began, not
// when the card object was built: slow agent startup must not delay the card
// past the threshold, and the clock keeps the true elapsed time.
func TestTurnCard_StartDelayAnchoredToTurnStart(t *testing.T) {
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangChinese)
	e.SetCardStartDelay(3 * time.Second)
	t.Cleanup(e.cancel)
	p := &taskCardStubPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}

	started := time.Now().Add(-5 * time.Second)
	tc := newTurnCardAt(e, p, "ctx", "k", started)
	if !tc.started.Equal(started) {
		t.Fatalf("started = %v, want the turn start %v", tc.started, started)
	}
	tc.Start()
	if sends := p.sendCount(); sends != 1 {
		t.Fatalf("card sends = %d, want 1: the threshold already elapsed during startup", sends)
	}
	if title := p.last().Header.Title; strings.Contains(title, "0:00") {
		t.Fatalf("title = %q, want elapsed time counted from the turn start", title)
	}
	tc.Finish(taskCardDone, "")
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

// EnsureStarted posts the card once; the engine no longer calls it on the
// first tool event (the start-delay timer is the only production trigger).
func TestTurnCardEnsureStartedPostsOnce(t *testing.T) {
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangChinese)
	t.Cleanup(e.cancel)
	p := &taskCardStubPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}

	tc := newTurnCard(e, p, "ctx", "k")
	tc.Start() // schedules the delayed post; nothing is sent yet
	if sends := p.sendCount(); sends != 0 {
		t.Fatalf("card sent before the start delay: %d", sends)
	}

	tc.EnsureStarted()
	if sends := p.sendCount(); sends != 1 {
		t.Fatalf("card sends = %d, want 1 after EnsureStarted", sends)
	}
	tc.EnsureStarted() // idempotent
	if sends := p.sendCount(); sends != 1 {
		t.Fatalf("card re-sent on repeat EnsureStarted: %d", sends)
	}
	tc.Finish(taskCardDone, "")
}

// Card steps keep the raw tool + command so the card stays a useful trace.
func TestTaskCardStepTextShowsRawCommand(t *testing.T) {
	cases := []struct{ name, tool, input, want string }{
		{"bash", "Bash", "git status", "Bash · git status"},
		{"shell_wrapper", "Bash", `bash -lc "go test ./..."`, "Bash · go test ./..."},
		{"drops_cd_prefix", "Bash", "cd /tmp/x && go test ./...", "Bash · go test ./..."},
		{"shortens_long_path", "Bash", "cat /var/folders/aa/bb/cc/project/file.go", "Bash · cat …/project/file.go"},
		{"shortens_home_path", "Bash", "ls ~/Documents/ObsidianNotes/AgentSync/cc-connect-arm64", "Bash · ls …/AgentSync/cc-connect-arm64"},
		{"shortens_dot_relative_path", "Bash", "cat ./demo-repo/src/components/profile.ts", "Bash · cat …/components/profile.ts"},
		{"shortens_path_in_flag", "Bash", "tool --config=/Users/me/projects/agent/config.toml run", "Bash · tool --config=…/agent/config.toml run"},
		{"shortens_quoted_path", "Bash", `node -e "require('/Users/me/projects/agent/lib/x.js')"`, `Bash · node -e "require('…/lib/x.js')"`},
		{"leaves_short_paths", "Bash", "ls /usr/local/bin", "Bash · ls /usr/local/bin"},
		{"leaves_urls", "Bash", "curl https://example.com/a/b/c/d", "Bash · curl https://example.com/a/b/c/d"},
		{"tool_only", "Read", "", "Read"},
		{"input_only", "", "ls -la", "ls -la"},
	}
	for _, c := range cases {
		if got := taskCardStepText(c.tool, c.input); got != c.want {
			t.Errorf("%s: taskCardStepText(%q, %q) = %q, want %q", c.name, c.tool, c.input, got, c.want)
		}
	}

	long := strings.Repeat("x", 200)
	got := taskCardStepText("Bash", long)
	if !strings.Contains(got, "…") || len([]rune(got)) > taskCardMaxStepLen {
		t.Errorf("long step not truncated to %d runes: %q", taskCardMaxStepLen, got)
	}
}

func TestCardInlineCode(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain", "hello [x](y) **b**", "`hello [x](y) **b**`"},
		{"collapses_newlines", "a\nb\tc", "`a b c`"},
		{"backtick_inside", "a `b`", "`` a `b` ``"},
	}
	for _, c := range cases {
		if got := cardInlineCode(c.in); got != c.want {
			t.Errorf("%s: cardInlineCode(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestTaskCardPreparingStaysOnOneLine(t *testing.T) {
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangChinese)
	t.Cleanup(e.cancel)
	p := &taskCardStubPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}

	tc := newTurnCard(e, p, "ctx", "k")
	tc.render(true)

	body := cardBody(t, p.last())
	if !strings.Contains(body, "**当前步骤：**准备中") {
		t.Fatalf("body = %q, want preparing on the label line", body)
	}
	if strings.Contains(body, "**当前步骤：**\n") {
		t.Fatalf("body = %q, preparing must not break the line", body)
	}
	tc.Finish(taskCardDone, "")
}

func TestTaskCardInlineStepAndQueue(t *testing.T) {
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangChinese)
	t.Cleanup(e.cancel)
	p := &taskCardStubPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}

	tc := newTurnCard(e, p, "ctx", "k")
	tc.SetStep("Bash · cat <at id=all></at>")
	tc.SetQueue([]string{"[点我](http://x) **急**"})
	tc.render(true)

	card := p.last()
	if card == nil || len(card.Elements) == 0 {
		t.Fatal("no card rendered")
	}
	md, ok := card.Elements[0].(CardMarkdown)
	if !ok {
		t.Fatalf("first element = %T, want CardMarkdown", card.Elements[0])
	}
	if !strings.Contains(md.Content, "**当前步骤：**`Bash · cat <at id=all></at>`") {
		t.Fatalf("step is not inline-coded verbatim: %q", md.Content)
	}
	if strings.Contains(md.Content, "**当前步骤：**\n") {
		t.Fatalf("step label must stay on one line: %q", md.Content)
	}
	if !strings.Contains(md.Content, "↳ 1. `[点我](http://x) **急**`") {
		t.Fatalf("queued markdown was not inlined literally: %q", md.Content)
	}
	tc.Finish(taskCardDone, "")
}

// The progress card carries progress only: the body is exactly the step line
// plus the queue lines, never model output or the reply text.
func TestTaskCardBodyCarriesOnlyProgress(t *testing.T) {
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangChinese)
	t.Cleanup(e.cancel)
	p := &taskCardStubPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}

	tc := newTurnCard(e, p, "ctx", "k")
	tc.SetStep("Bash · git status")
	tc.SetQueue([]string{"再来一个"})
	tc.render(true)

	want := "**当前步骤：**`Bash · git status`\n📋 排队中 (1)\n↳ 1. `再来一个`"
	if body := cardBody(t, p.last()); body != want {
		t.Fatalf("body = %q, want exactly the progress lines %q", body, want)
	}
	tc.Finish(taskCardDone, "")
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

// The ticker must keep refreshing while the agent is active: the elapsed clock
// is user-visible and must not freeze between events.
func TestTurnCard_TickerRefreshesWhileAgentActive(t *testing.T) {
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangChinese)
	e.SetCardTickInterval(20 * time.Millisecond)
	t.Cleanup(e.cancel)
	p := &taskCardStubPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}

	tc := newTurnCard(e, p, "ctx", "k")
	tc.EnsureStarted()
	time.Sleep(10 * time.Millisecond) // let the create land

	before := p.updateCount()
	time.Sleep(80 * time.Millisecond) // several ticks
	if got := p.updateCount(); got <= before {
		t.Fatalf("card did not refresh during an active turn: %d -> %d", before, got)
	}
	tc.Finish(taskCardDone, "")
}

// The ticker must keep a steady cadence: several ticks inside a short window,
// not just one refresh followed by silence.
func TestTurnCard_TickerCadence(t *testing.T) {
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangChinese)
	e.SetCardTickInterval(50 * time.Millisecond)
	t.Cleanup(e.cancel)
	p := &taskCardStubPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}

	tc := newTurnCard(e, p, "ctx", "k")
	tc.EnsureStarted()
	time.Sleep(400 * time.Millisecond)
	updates := p.updateCount()
	tc.Finish(taskCardDone, "")

	// ~8 ticks expected; allow scheduling jitter and one coalesced render.
	if updates < 5 {
		t.Fatalf("UpdateTaskCard calls = %d in 400ms at 50ms tick, want a steady cadence", updates)
	}
}

// Regression: the card can become visible before EnsureStarted (early queue or
// step render). The ticker must still start, otherwise elapsed time freezes for
// the rest of the turn.
func TestTurnCard_TickerStartsWhenCardAlreadyVisible(t *testing.T) {
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangChinese)
	e.SetCardTickInterval(20 * time.Millisecond)
	t.Cleanup(e.cancel)
	p := &taskCardStubPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}

	tc := newTurnCard(e, p, "ctx", "k")
	tc.render(true) // card forced visible before EnsureStarted runs
	if !tc.Visible() {
		t.Fatal("card should be visible after a forced render")
	}

	tc.EnsureStarted()
	before := p.updateCount()
	time.Sleep(90 * time.Millisecond)
	if got := p.updateCount(); got <= before {
		t.Fatalf("ticker did not start for an already-visible card: %d -> %d", before, got)
	}
	tc.Finish(taskCardDone, "")
}

// A task card replaces the "正在思考..." placeholder entirely: the card posts
// on its start-delay timer even before the first event, so the placeholder
// must never be sent on card-capable platforms. Platforms without a task card
// keep the loading animation.
func TestShouldPreStartLoadingSuppressedByTaskCard(t *testing.T) {
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangChinese)
	t.Cleanup(e.cancel)
	p := &taskCardStubPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	tc := newTurnCard(e, p, "ctx", "k")
	state := &interactiveState{taskCard: tc, platform: p, replyCtx: "ctx"}

	if e.shouldPreStartLoading(state, p) {
		t.Fatal("loading placeholder must be suppressed while a live task card exists")
	}
	tc.render(true)
	if e.shouldPreStartLoading(state, p) {
		t.Fatal("loading placeholder must stay suppressed once the card is visible")
	}
	tc.Finish(taskCardDone, "")
	// A finished card from an earlier turn must not suppress the placeholder:
	// proactive turns create no new card and rely on it.
	if !e.shouldPreStartLoading(state, p) {
		t.Fatal("loading placeholder must come back once the task card is finished")
	}

	plain := &stubPlatformEngine{n: "plain"}
	plainState := &interactiveState{platform: plain, replyCtx: "ctx"}
	if !e.shouldPreStartLoading(plainState, plain) {
		t.Fatal("platforms without a task card must keep the loading animation")
	}
}

// Neither an empty queue nor a pre-threshold step may create the "准备中"
// card; the delayed start owns the first post.
func TestSyncTaskCardQueueEmptyKeepsCardLazy(t *testing.T) {
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangChinese)
	t.Cleanup(e.cancel)
	p := &taskCardStubPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}

	tc := newTurnCard(e, p, "ctx", "k")
	state := &interactiveState{taskCard: tc}
	e.syncTaskCardQueue(state)
	if sends := p.sendCount(); sends != 0 {
		t.Fatalf("card created for an empty queue: %d", sends)
	}

	tc.SetStep("Bash · true")
	if sends := p.sendCount(); sends != 0 {
		t.Fatalf("card sends = %d, want 0 before the start threshold", sends)
	}

	tc.EnsureStarted()
	if sends := p.sendCount(); sends != 1 {
		t.Fatalf("card sends = %d, want 1 after EnsureStarted", sends)
	}
	tc.Finish(taskCardDone, "")
}

// logHandle is a stub card handle that exposes a message id, mirroring the
// feishu handle used for log correlation.
type logHandle string

func (h logHandle) TaskCardMessageID() string { return string(h) }

// TestTaskCardHandleID locks the log-correlation helper: handles that expose a
// message id are reported, everything else degrades to an empty string.
func TestTaskCardHandleID(t *testing.T) {
	if got := taskCardHandleID(nil); got != "" {
		t.Fatalf("nil handle id = %q, want empty", got)
	}
	if got := taskCardHandleID("handle-1"); got != "" {
		t.Fatalf("plain handle id = %q, want empty", got)
	}
	if got := taskCardHandleID(logHandle("om_123")); got != "om_123" {
		t.Fatalf("handle id = %q, want om_123", got)
	}
}

// TestShouldAppendTerminalReceipt locks the staleness rule: the reply restates
// the terminal state only when the card's last accepted frame is old enough
// that the client is likely showing an outdated elapsed time. Fresh cards stay
// silent so healthy turns do not repeat "done" twice.
func TestShouldAppendTerminalReceipt(t *testing.T) {
	fresh := func() *turnCard {
		return &turnCard{visible: true, tickInterval: 5 * time.Second, lastRenderOK: time.Now()}
	}
	cases := []struct {
		name string
		card *turnCard
		want bool
	}{
		{"nil card", nil, false},
		{"card never visible", &turnCard{tickInterval: 5 * time.Second, lastRenderOK: time.Now().Add(-time.Minute)}, false},
		{"ticker disabled", &turnCard{visible: true, lastRenderOK: time.Now().Add(-time.Minute)}, false},
		{"no accepted frame", &turnCard{visible: true, tickInterval: 5 * time.Second}, false},
		{"fresh frame", fresh(), false},
		{"one tick behind", &turnCard{visible: true, tickInterval: 5 * time.Second, lastRenderOK: time.Now().Add(-6 * time.Second)}, false},
		{"two ticks behind", &turnCard{visible: true, tickInterval: 5 * time.Second, lastRenderOK: time.Now().Add(-12 * time.Second)}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldAppendTerminalReceipt(c.card); got != c.want {
				t.Fatalf("shouldAppendTerminalReceipt() = %v, want %v", got, c.want)
			}
		})
	}
}

// TestEngineTerminalReceipt covers the reply restatement end to end at the
// helper level: only a live, visible card whose last accepted frame is stale
// produces a line, and the line carries the real duration.
func TestEngineTerminalReceipt(t *testing.T) {
	e := NewEngine("test", &resultAgent{session: newResultAgentSession("ok")}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangChinese)
	t.Cleanup(e.cancel)

	cases := []struct {
		name string
		card *turnCard
		want string
	}{
		{"no card", nil, ""},
		{"fresh card", &turnCard{visible: true, tickInterval: 5 * time.Second, lastRenderOK: time.Now()}, ""},
		{"finished card", &turnCard{visible: true, finished: true, tickInterval: 5 * time.Second, lastRenderOK: time.Now().Add(-time.Minute)}, ""},
		{"stale card", &turnCard{visible: true, tickInterval: 5 * time.Second, lastRenderOK: time.Now().Add(-20 * time.Second)}, "✅ 已完成 · 用时 2:43"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := e.terminalReceipt(c.card, 2*time.Minute+43*time.Second); got != c.want {
				t.Fatalf("terminalReceipt() = %q, want %q", got, c.want)
			}
		})
	}
}
