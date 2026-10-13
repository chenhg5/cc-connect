package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type queryTestCard struct {
	mu              sync.Mutex
	content, notice string
	prompt          *Card
	done            bool
	status          CardStatus
	split           bool
	onAnswer        func(string)
}

func (c *queryTestCard) Update(_ context.Context, text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return nil
	}
	c.content = text
	return nil
}
func (c *queryTestCard) Finalize(ctx context.Context, text string) error {
	return c.Finish(ctx, text, CardStatusDone)
}
func (c *queryTestCard) Finish(_ context.Context, text string, status CardStatus) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return nil
	}
	if text != "" {
		c.content = text
	}
	c.done = true
	c.status = status
	c.prompt = nil
	return nil
}
func (c *queryTestCard) Failed() bool              { return false }
func (c *queryTestCard) SeparateFinalAnswer() bool { return c.split }
func (c *queryTestCard) FinalizeWithAnswer(_ context.Context, progress, answer string) error {
	c.mu.Lock()
	if c.done {
		c.mu.Unlock()
		return nil
	}
	c.content, c.prompt, c.done, c.status = progress, nil, true, CardStatusDone
	c.mu.Unlock()
	if answer != "" {
		c.onAnswer(answer)
	}
	return nil
}
func (c *queryTestCard) SetPrompt(_ context.Context, card *Card) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return nil
	}
	c.prompt = card
	return nil
}
func (c *queryTestCard) SetNotice(_ context.Context, text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return nil
	}
	c.notice = text
	return nil
}

func TestQueryCard_TerminalStates(t *testing.T) {
	for _, action := range []string{"/stop", "/cancel", "error", "closed"} {
		t.Run(action, func(t *testing.T) {
			p := &queryTestPlatform{split: true, stubCardPlatform: stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}}
			a := &cujAgent{}
			a.setNextSessionEvents([]Event{{Type: EventText, Content: "Partial output"}}, 0)
			e := NewEngine("test", a, []Platform{p}, t.TempDir()+"/sessions.json", LangEnglish)
			defer e.Stop()
			send := func(content string) {
				e.ReceiveMessage(p, &Message{SessionKey: "test:user", UserID: "user", Platform: "test", ReplyCtx: "ctx", Content: content})
			}
			send("start task")
			awaitQuery(t, func() bool {
				cs := p.snapshots()
				if len(cs) != 1 {
					return false
				}
				text, _, _ := cs[0].snapshot()
				return strings.Contains(text, "Partial output")
			})
			if strings.HasPrefix(action, "/") {
				send(action)
			} else {
				e.interactiveMu.Lock()
				state := e.interactiveStates["test:user"]
				e.interactiveMu.Unlock()
				state.mu.Lock()
				as := state.agentSession.(*cujAgentSession)
				state.mu.Unlock()
				if action == "error" {
					as.events <- Event{Type: EventError, Error: errors.New("test failure")}
				} else {
					close(as.events)
				}
			}
			awaitQuery(t, func() bool { _, _, done := p.snapshots()[0].snapshot(); return done })
			if len(p.snapshots()) != 1 {
				t.Fatal("terminal state created a conclusion card")
			}
			c := p.snapshots()[0]
			c.mu.Lock()
			status, notice := c.status, c.notice
			c.mu.Unlock()
			if (action == "error" || action == "closed") && status != CardStatusError {
				t.Fatalf("status=%s", status)
			}
			if action == "/cancel" && !strings.Contains(notice, "cancel") {
				t.Fatalf("cancel notice=%s", notice)
			}
			if got := p.getSent(); len(got) != 0 {
				t.Fatalf("terminal state sent extra messages: %v", got)
			}
		})
	}
}

func (c *queryTestCard) snapshot() (string, *Card, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.content, c.prompt, c.done
}

type queryTestPlatform struct {
	stubCardPlatform
	cardsMu sync.Mutex
	cards   []*queryTestCard
	split   bool
}

func (p *queryTestPlatform) SingleQueryCardEnabled() bool { return true }

func (p *queryTestPlatform) BuildRichCard(CardStatus, string, []ToolStep, string, bool, string) string {
	panic("a query card must take precedence over the separate rich card path")
}

func TestQueryCard_UndisplayablePromptDeniesRequest(t *testing.T) {
	for _, card := range []InteractiveStreamingCard{nil, &queryTestCard{}} {
		t.Run(fmt.Sprintf("card=%t", card != nil), func(t *testing.T) {
			as := &recordingAgentSession{}
			e := NewEngine("test", &stubAgent{}, nil, t.TempDir()+"/sessions.json", LangEnglish)
			defer e.Stop()
			pending := &pendingPermission{RequestID: "unshown", Card: card, Resolved: make(chan struct{})}
			state := &interactiveState{agentSession: as, pending: pending}
			e.failQueryPrompt(state, pending, errors.New("card update failed"))
			if as.calls != 1 || as.lastID != "unshown" || as.lastResult.Behavior != "deny" {
				t.Fatal("undisplayable prompt must be denied")
			}
			select {
			case <-pending.Resolved:
			default:
				t.Fatal("undisplayable prompt left agent waiting")
			}
			if state.pending != nil {
				t.Fatal("failed prompt retained active approval")
			}
		})
	}
}
func (p *queryTestPlatform) CreateStreamingCard(context.Context, any) (StreamingCard, error) {
	p.cardsMu.Lock()
	defer p.cardsMu.Unlock()
	c := &queryTestCard{split: p.split, onAnswer: func(answer string) {
		p.cardsMu.Lock()
		defer p.cardsMu.Unlock()
		p.cards = append(p.cards, &queryTestCard{content: answer, done: true, status: CardStatusDone})
	}}
	p.cards = append(p.cards, c)
	return c, nil
}
func (p *queryTestPlatform) snapshots() []*queryTestCard {
	p.cardsMu.Lock()
	defer p.cardsMu.Unlock()
	return append([]*queryTestCard(nil), p.cards...)
}
func awaitQuery(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("query card condition not reached")
}

func TestQueryCard_SplitSilentReply(t *testing.T) {
	p := &queryTestPlatform{split: true, stubCardPlatform: stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}}
	a := &cujAgent{}
	a.setNextSessionEvents([]Event{{Type: EventThinking, Content: "Visible summary"}, {Type: EventText, Content: "NO_REPLY"}, {Type: EventResult, Content: "NO_REPLY", Done: true}}, 0)
	e := NewEngine("test", a, []Platform{p}, t.TempDir()+"/sessions.json", LangEnglish)
	defer e.Stop()
	e.SetDisplayConfig(DisplayCfg{ThinkingMessages: true})
	e.ReceiveMessage(p, &Message{SessionKey: "test:user", UserID: "user", Platform: "test", ReplyCtx: "ctx", Content: "start"})
	awaitQuery(t, func() bool {
		cs := p.snapshots()
		if len(cs) == 0 {
			return false
		}
		_, _, done := cs[0].snapshot()
		return done
	})
	if len(p.snapshots()) != 1 || len(p.getSent()) != 0 {
		t.Fatal("silent reply produced a conclusion")
	}
	content, _, _ := p.snapshots()[0].snapshot()
	if strings.Contains(content, "NO_REPLY") || !strings.Contains(content, "Visible summary") {
		t.Fatalf("incorrect silent process: %q", content)
	}
}
