package core

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// failingAfterFirstUpdateCard mirrors feishuStreamingCard when the platform
// reports a card failure after the card was already created: the first update
// succeeds, then Failed() flips to true.
type failingAfterFirstUpdateCard struct {
	mu      sync.Mutex
	updates int
}

func (c *failingAfterFirstUpdateCard) Update(_ context.Context, _ string) error {
	c.mu.Lock()
	c.updates++
	c.mu.Unlock()
	return nil
}

func (c *failingAfterFirstUpdateCard) Finalize(_ context.Context, _ string) error { return nil }

func (c *failingAfterFirstUpdateCard) Failed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.updates >= 1
}

type failingStreamCardPlatform struct {
	stubCompactProgressPlatform
	card *failingAfterFirstUpdateCard
}

func (p *failingStreamCardPlatform) CreateStreamingCard(_ context.Context, _ any) (StreamingCard, error) {
	p.card = &failingAfterFirstUpdateCard{}
	return p.card, nil
}

// failingCardAgentSession emits two thinking snapshots (the shape the opencode
// adapter produces: one EventThinking per reasoning part update), then an
// answer.
type failingCardAgentSession struct {
	events chan Event
}

func newFailingCardAgentSession() *failingCardAgentSession {
	return &failingCardAgentSession{events: make(chan Event, 16)}
}

func (s *failingCardAgentSession) Send(_ string, _ string, _ []ImageAttachment, _ []FileAttachment) error {
	go func() {
		s.events <- Event{Type: EventThinking, Content: "第一步：查看运行记录"}
		s.events <- Event{Type: EventThinking, Content: "第二步：核对 token"}
		s.events <- Event{Type: EventText, Content: "完成。"}
		s.events <- Event{Type: EventResult, Content: "完成。", Done: true}
	}()
	return nil
}

func (s *failingCardAgentSession) RespondPermission(_ string, _ PermissionResult) error { return nil }
func (s *failingCardAgentSession) Events() <-chan Event                                 { return s.events }
func (s *failingCardAgentSession) CurrentSessionID() string                             { return "failing-card" }
func (s *failingCardAgentSession) Alive() bool                                          { return true }
func (s *failingCardAgentSession) Close() error                                         { return nil }

// TestStreamingCard_FailedMidTurn_FallsBackToAggregatedProgressCard is a
// regression test for the "one card per thinking snapshot" bug: once the
// streaming card fails mid-turn, the compact progress writer (disabled at turn
// start because the streaming card existed) must take over, so the remaining
// events update one progress card instead of posting a standalone message for
// every thinking snapshot.
func TestStreamingCard_FailedMidTurn_FallsBackToAggregatedProgressCard(t *testing.T) {
	p := &failingStreamCardPlatform{
		stubCompactProgressPlatform: stubCompactProgressPlatform{
			stubPlatformEngine: stubPlatformEngine{n: "feishu"},
			style:              "card",
			supportPayload:     true,
		},
	}
	agent := &resultAgent{session: newFailingCardAgentSession()}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.SetDisplayConfig(DisplayCfg{
		ThinkingMessages: true,
		ThinkingMaxLen:   300,
		ToolMaxLen:       500,
		ToolMessages:     true,
		Mode:             "full",
		CardMode:         "legacy",
	})

	msg := &Message{
		SessionKey: "feishu:failing-card",
		Platform:   "feishu",
		UserID:     "u1",
		UserName:   "user",
		Content:    "hello",
		ReplyCtx:   "ctx",
	}
	e.handleMessage(p, msg)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(strings.Join(p.getSent(), "\n"), "完成。") {
			time.Sleep(100 * time.Millisecond)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	sent := strings.Join(p.getSent(), "\n")
	if strings.Contains(sent, "💭") {
		t.Errorf("thinking snapshots leaked as standalone messages after the card failed: %q", sent)
	}
	if starts := p.getPreviewStarts(); len(starts) != 1 {
		t.Errorf("want exactly one aggregated progress card, got %d: %q", len(starts), starts)
	}
	if edits := p.getPreviewEdits(); len(edits) == 0 {
		t.Error("the aggregated progress card was never updated with the later thinking snapshot")
	} else if joined := strings.Join(edits, "\n"); !strings.Contains(joined, "第二步") {
		t.Errorf("later thinking snapshot missing from the aggregated progress card: %q", joined)
	}
	if !strings.Contains(sent, "完成。") {
		t.Errorf("final answer missing; sent=%q", sent)
	}
}
