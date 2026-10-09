package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type observedAsyncCardPlatform struct{ stubCardPlatform }

func (p *observedAsyncCardPlatform) SendCard(ctx context.Context, reply any, card *Card) error {
	if err := p.stubCardPlatform.SendCard(ctx, reply, card); err != nil {
		return err
	}
	data, _ := json.Marshal(card)
	return p.Send(ctx, reply, string(data))
}

func asyncCardAction(t *testing.T, p *observedAsyncCardPlatform, label string) string {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, card := range p.sentCards {
		for _, element := range card.Elements {
			if actions, ok := element.(CardActions); ok {
				for _, button := range actions.Buttons {
					if button.Text == label {
						return strings.TrimPrefix(button.Value, "cmd:")
					}
				}
			}
		}
	}
	t.Fatalf("missing button %s", label)
	return ""
}

func TestSharedAsyncQuestionFreeTextRetriesAndDeduplicates(t *testing.T) {
	env := newSharedTestEnv(t)
	env.send("/attach first")
	env.await("Attached to session first")
	as := env.a.connection("first")
	env.e.interactiveMu.Lock()
	state := env.e.interactiveStates["test:user"]
	env.e.interactiveMu.Unlock()
	event := Event{Type: EventText, ItemID: "question", TurnID: "active-first", Content: "tail", Questions: []UserQuestion{{Question: "Preference?"}}, Metadata: map[string]any{"delivery": "async", "message_text": "COMPLETE ASYNC EXPLANATION"}}
	env.e.showSharedAsyncQuestions(state, as, event)
	env.await("COMPLETE ASYNC EXPLANATION")
	env.e.showSharedAsyncQuestions(state, as, event)
	env.p.mu.Lock()
	count := len(env.p.sentCards)
	env.p.mu.Unlock()
	if count != 1 {
		t.Fatalf("duplicate cards: %d", count)
	}
	state.mu.Lock()
	var token string
	for k := range state.shared.asyncQuestions {
		token = k
	}
	pending := state.pending
	state.mu.Unlock()
	if pending != nil {
		t.Fatal("async question blocked approvals")
	}
	as.mu.Lock()
	as.steerError = context.DeadlineExceeded
	as.mu.Unlock()
	env.send("/async-answer " + token + " text one two")
	env.await("context deadline exceeded")
	as.mu.Lock()
	as.steerError = nil
	as.mu.Unlock()
	env.send("/async-answer " + token + " text one two")
	env.await("STEER observed: Preference?: one two")
	env.send("/async-answer " + token + " text duplicate")
	env.await(env.e.i18n.T(MsgSharedStaleRequest))
}

func TestSharedAsyncQuestionMalformedAndUnscopedCannotCreateCards(t *testing.T) {
	env := newSharedTestEnv(t)
	env.send("/attach first")
	env.await("Attached to session first")
	as := env.a.connection("first")
	as.emit(Event{Type: EventText, Content: "ASYNC WITHOUT STRUCTURE", Metadata: map[string]any{"delivery": "async", "phase": "commentary"}})
	env.await("ASYNC WITHOUT STRUCTURE")
	env.p.mu.Lock()
	defer env.p.mu.Unlock()
	if len(env.p.sentCards) != 0 {
		t.Fatal("fabricated card without questions")
	}
}

func waitAsyncVisible(t *testing.T, p *observedAsyncCardPlatform, text string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(strings.Join(p.getSent(), "\n"), text) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("missing %q: %v", text, p.getSent())
}

func TestSharedAsyncQuestionForegroundCardDoesNotWaitForToolBoundary(t *testing.T) {
	env := newSharedTestEnv(t)
	env.send("/attach first")
	env.await("Attached to session first")
	as := env.a.connection("first")
	env.e.interactiveMu.Lock()
	state := env.e.interactiveStates["test:user"]
	env.e.interactiveMu.Unlock()
	ctx, cancel := context.WithCancel(env.e.ctx)
	defer cancel()
	f := &sharedForeground{ctx: ctx, cancel: cancel, events: make(chan Event, 4), bound: true, turnID: "active-first"}
	state.mu.Lock()
	state.shared.foreground = f
	state.mu.Unlock()
	as.emit(Event{Type: EventText, TurnID: "active-first", ItemID: "q", Questions: []UserQuestion{{Question: "Foreground free question?"}}, Metadata: map[string]any{"delivery": "async"}})
	env.await("Foreground free question?")
	select {
	case event := <-f.events:
		if len(event.Questions) != 1 {
			t.Fatal("question text stream lost metadata")
		}
	case <-time.After(time.Second):
		t.Fatal("foreground event lost")
	}
	state.mu.Lock()
	var token string
	for k := range state.shared.asyncQuestions {
		token = k
	}
	state.mu.Unlock()
	as.mu.Lock()
	as.turn = "active-next"
	as.mu.Unlock()
	env.send("/async-answer " + token + " text late answer")
	env.await(env.e.i18n.T(MsgSharedStaleRequest))
	// No fallback starts another turn or steers the new one.
	select {
	case event := <-f.events:
		if strings.Contains(event.Content, "late answer") {
			t.Fatal("stale answer reached new turn")
		}
	default:
	}
}
