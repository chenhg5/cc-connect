package core

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

type taskCardStubPlatform struct {
	stubPlatformEngine
	mu      sync.Mutex
	sends   []*Card
	updates []*Card
	failSend bool
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
	p.updates = append(p.updates, card)
	return nil
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
