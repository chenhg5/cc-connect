package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func assertSingleQueuedChoice(t *testing.T, card *Card) {
	t.Helper()
	count := 0
	for _, element := range card.Elements {
		if row, ok := element.(CardActions); ok {
			for _, button := range row.Buttons {
				count++
				if !strings.HasPrefix(button.Value, "cmd:/ps --queued ") {
					t.Fatalf("queue receipt has a redundant action: %s", button.Value)
				}
			}
		}
	}
	if count != 1 {
		t.Fatalf("queue receipt has %d actions, want only immediate delivery", count)
	}
}

func TestQueuedSteering_DrainUpdatesUnclickedCard(t *testing.T) {
	p := &trackedQueuedChoicePlatform{}
	p.n = "test"
	s := &asyncInputSession{events: make(chan Event, 8), active: true}
	e := NewEngine("test", &controllableAgent{nextSession: s}, []Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	const key = "test:drain"
	session := e.sessions.GetOrCreateActive(key)
	gen, _ := session.TryLock()
	state := &interactiveState{agentSession: s, busySession: session}
	e.interactiveStates[key] = state
	if !e.queueMessageForBusySession(p, &Message{SessionKey: key, UserID: "author", Content: "queued task", ReplyCtx: "user-message"}, key) {
		t.Fatal("message was not queued")
	}
	done := make(chan bool, 1)
	go func() { done <- e.drainPendingMessages(state, session, e.sessions, key, gen) }()
	waitAsyncInput(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return len(s.sent) == 1 })
	updates := p.getChoiceUpdates()
	if len(updates) != 1 || updates[0].ctx != "sent-card-1" {
		t.Fatal("drain did not update its unclicked card")
	}
	assertClosedQueuedChoice(t, updates[0].card, e.i18n.T(MsgQueueProcessing), "queued task")
	s.events <- Event{Type: EventResult, Content: "task complete", Done: true}
	select {
	case unlocked := <-done:
		if !unlocked {
			t.Fatal("drain did not release session")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("drain did not complete")
	}
}

func TestQueuedSteering_LateKeepDoesNotOverwriteAutomaticDelivery(t *testing.T) {
	p := &trackedQueuedChoicePlatform{}
	p.n = "test"
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	queued := queuedMessage{platform: p, content: "queued task", choiceReceipt: &queuedChoiceReceipt{replyCtx: "sent-card-1", platform: p, content: "queued task"}}
	e.markQueuedMessageProcessing(queued)
	e.finishQueuedMessageChoice(p, &Message{ReplyCtx: "clicked-card"}, queued, MsgQueueKept, MsgMessageQueued, "blue")
	updates := p.getChoiceUpdates()
	if len(updates) != 1 {
		t.Fatal("late Keep replaced the processing status")
	}
	assertClosedQueuedChoice(t, updates[0].card, e.i18n.T(MsgQueueProcessing), "queued task")
}

func TestQueuedSteering_TrackedSendFailureRetainsQueuedMessage(t *testing.T) {
	p := &trackedQueuedChoicePlatform{trackErr: errors.New("send unavailable")}
	p.n = "test"
	s := &asyncInputSession{events: make(chan Event, 8), active: true}
	e := NewEngine("test", &controllableAgent{nextSession: s}, []Platform{p}, "", LangEnglish)
	const key = "test:send-failure"
	state := &interactiveState{agentSession: s}
	e.interactiveStates[key] = state
	if !e.queueMessageForBusySession(p, &Message{SessionKey: key, UserID: "author", Content: "queued task", ReplyCtx: "user-message"}, key) {
		t.Fatal("card send failure rejected the message")
	}
	if len(state.pendingMessages) != 1 || state.pendingMessages[0].choiceReceipt != nil || !strings.Contains(strings.Join(p.getSent(), "\n"), e.i18n.T(MsgMessageQueued)) {
		t.Fatal("card send failure lost the queue or its text receipt")
	}
}

type queuedChoiceCardUpdate struct {
	ctx  any
	card *Card
}

type queuedChoicePlatform struct {
	asyncInputPlatform
	updates   []queuedChoiceCardUpdate
	updateErr error
}

func (p *queuedChoicePlatform) UpdateActionCard(_ context.Context, ctx any, card *Card) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.updateErr != nil {
		return false, p.updateErr
	}
	if ctx != "clicked-card" && !strings.HasPrefix(fmt.Sprint(ctx), "sent-card-") {
		return false, nil
	}
	p.updates = append(p.updates, queuedChoiceCardUpdate{ctx: ctx, card: card})
	return true, nil
}

type trackedQueuedChoicePlatform struct {
	queuedChoicePlatform
	cardCount int
	trackErr  error
}

func (p *trackedQueuedChoicePlatform) SendTrackedCard(ctx context.Context, replyCtx any, card *Card) (any, error) {
	if p.trackErr != nil {
		return nil, p.trackErr
	}
	if err := p.SendCard(ctx, replyCtx, card); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cardCount++
	return fmt.Sprintf("sent-card-%d", p.cardCount), nil
}

func (p *queuedChoicePlatform) getChoiceUpdates() []queuedChoiceCardUpdate {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]queuedChoiceCardUpdate(nil), p.updates...)
}

func assertClosedQueuedChoice(t *testing.T, card *Card, status, content string) {
	t.Helper()
	if !strings.Contains(card.RenderText(), status) || !strings.Contains(card.RenderText(), content) {
		t.Fatalf("choice card lost its status or message: %s", card.RenderText())
	}
	for _, element := range card.Elements {
		if _, ok := element.(CardActions); ok {
			t.Fatal("processed choice card still has clickable buttons")
		}
	}
}

func TestQueuedSteering_ReplacesClickedCardAndRemovesButtons(t *testing.T) {
	for _, scenario := range []string{"keep", "delivered", "idle", "unconfirmed", "update-error", "typed-command", "other-user"} {
		t.Run(scenario, func(t *testing.T) {
			p := &queuedChoicePlatform{}
			p.n = "test"
			s := &queuedSteeringSession{asyncInputSession: asyncInputSession{events: make(chan Event, 8), active: true}}
			e := NewEngine("test", &controllableAgent{nextSession: s}, []Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
			t.Cleanup(func() { _ = e.Stop() })
			const key = "test:chat:user"
			session := e.sessions.GetOrCreateActive(key)
			gen, _ := session.TryLock()
			defer session.Unlock(gen)
			const content = "Use staging instead"
			state := &interactiveState{agentSession: s, busySession: session, pendingMessages: []queuedMessage{
				{queueID: "one", content: content, userID: "author"},
			}}
			e.interactiveStates[key] = state
			msg := &Message{SessionKey: key, UserID: "author", ReplyCtx: "clicked-card"}
			status := MsgPsSent
			switch scenario {
			case "keep":
				status = MsgMessageQueued
			case "idle":
				s.active = false
				status = MsgMessageQueued
			case "unconfirmed":
				s.err = errors.New("acknowledgement lost")
				status = MsgQueueSteerUnconfirmed
			case "update-error":
				p.updateErr = errors.New("patch unavailable")
			case "typed-command":
				msg.ReplyCtx = "user-message"
			case "other-user":
				msg.UserID = "someone-else"
			}
			if scenario == "delivered" {
				s.steer = func(string) (bool, error) {
					updates := p.getChoiceUpdates()
					if len(updates) != 1 {
						t.Fatalf("buttons were not removed before steering: %#v", updates)
					}
					assertClosedQueuedChoice(t, updates[0].card, e.i18n.T(MsgQueueSteerPending), content)
					// A replay while the adapter is busy must not replace the
					// processing card or deliver the message a second time.
					e.handleQueuedMessageChoice(p, msg, "one", true)
					return true, nil
				}
			}
			e.handleQueuedMessageChoice(p, msg, "one", scenario == "keep")
			updates := p.getChoiceUpdates()
			if scenario == "other-user" {
				if len(updates) != 0 || len(s.steered) != 0 || state.pendingMessages[0].queueChoiceClosed {
					t.Fatal("another user modified the author's choice")
				}
				return
			}
			if scenario == "update-error" || scenario == "typed-command" {
				if len(updates) != 0 || len(s.steered) != 1 || !strings.Contains(strings.Join(p.getSent(), "\n"), e.i18n.T(status)) {
					t.Fatal("card update failure lost the delivery or text receipt")
				}
				return
			}
			want := 2
			if scenario == "keep" {
				want = 1
			}
			if len(updates) != want {
				t.Fatalf("got %d card updates, want %d", len(updates), want)
			}
			assertClosedQueuedChoice(t, updates[len(updates)-1].card, e.i18n.T(status), content)
			e.handleQueuedMessageChoice(p, msg, "one", false)
			e.handleQueuedMessageChoice(p, msg, "one", true)
			if len(p.getChoiceUpdates()) != want {
				t.Fatal("repeat clicks overwrote the final card")
			}
		})
	}
}

type queuedSteeringSession struct {
	asyncInputSession
	steer func(string) (bool, error)
}

func (s *queuedSteeringSession) SteerUserInput(prompt string) (bool, error) {
	if s.steer != nil {
		return s.steer(prompt)
	}
	return s.asyncInputSession.SteerUserInput(prompt)
}

func queuedChoiceAction(t *testing.T, card *Card, keep bool) string {
	t.Helper()
	prefix := "cmd:/ps --queued "
	for _, element := range card.Elements {
		if row, ok := element.(CardActions); ok {
			for _, button := range row.Buttons {
				if strings.HasPrefix(button.Value, prefix) {
					if keep {
						// Old cards may still send Keep callbacks after upgrading.
						return "/ps --keep " + strings.TrimPrefix(button.Value, prefix)
					}
					return strings.TrimPrefix(button.Value, "cmd:")
				}
			}
		}
	}
	t.Fatalf("missing %s button in %#v", prefix, card)
	return ""
}

func TestQueuedSteering_KeepOwnershipAndUnconfirmedDelivery(t *testing.T) {
	for _, scenario := range []string{"keep", "other-user", "anonymous", "wrong-session", "uncertain", "idle", "stopped"} {
		t.Run(scenario, func(t *testing.T) {
			p := &stubPlatformEngine{n: "test"}
			s := &queuedSteeringSession{asyncInputSession: asyncInputSession{events: make(chan Event, 8), active: true}}
			e := NewEngine("test", &controllableAgent{nextSession: s}, []Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
			key := "test:chat:user"
			session := e.sessions.GetOrCreateActive(key)
			gen, locked := session.TryLock()
			if !locked {
				t.Fatal("lock session")
			}
			t.Cleanup(func() { session.Unlock(gen) })
			state := &interactiveState{agentSession: s, busySession: session, pendingMessages: []queuedMessage{
				{queueID: "one", content: "first", userID: "user"},
				{queueID: "two", content: "second", userID: "user"},
			}}
			e.interactiveStates[key] = state
			msg := &Message{SessionKey: key, UserID: "user", ReplyCtx: "ctx"}
			switch scenario {
			case "other-user":
				msg.UserID = "someone-else"
			case "anonymous":
				msg.UserID = ""
			case "wrong-session":
				msg.SessionKey = "test:another-chat:user"
			case "uncertain":
				s.err = errors.New("acknowledgement lost")
			case "idle":
				s.active = false
			case "stopped":
				state.stopped = true
			}
			e.handleQueuedMessageChoice(p, msg, "one", scenario == "keep")
			state.mu.Lock()
			defer state.mu.Unlock()
			if scenario == "uncertain" {
				if len(state.pendingMessages) != 1 || state.pendingMessages[0].content != "second" {
					t.Fatalf("uncertain input must not be replayed: %#v", state.pendingMessages)
				}
				history := session.GetHistory(10)
				if len(history) != 1 || history[0].Content != "first" {
					t.Fatalf("unconfirmed input missing from history: %#v", history)
				}
				if !strings.Contains(strings.Join(p.getSent(), "\n"), e.i18n.T(MsgQueueSteerUnconfirmed)) {
					t.Fatal("missing unconfirmed-delivery notice")
				}
				return
			}
			if len(state.pendingMessages) != 2 || state.pendingMessages[0].content != "first" || state.pendingMessages[1].content != "second" {
				t.Fatalf("queue lost or reordered: %#v", state.pendingMessages)
			}
			if len(s.steered) != 0 {
				t.Fatal("unexpected steering")
			}
			if scenario == "keep" && !state.pendingMessages[0].queueChoiceClosed {
				t.Fatal("keep must close the choice without removing the message")
			}
		})
	}
}

func TestQueuedSteering_ClaimBeforeRPCAndRestoreAfterConcurrentDrain(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	s := &queuedSteeringSession{asyncInputSession: asyncInputSession{events: make(chan Event, 8), active: true}}
	e := NewEngine("test", &controllableAgent{nextSession: s}, []Platform{p}, "", LangEnglish)
	const key = "test:user"
	session := e.sessions.GetOrCreateActive(key)
	gen, _ := session.TryLock()
	defer session.Unlock(gen)
	state := &interactiveState{agentSession: s, busySession: session, pendingMessages: []queuedMessage{
		{queueID: "first", content: "first", userID: "user"},
		{queueID: "chosen", content: "chosen", userID: "user"},
		{queueID: "last", content: "last", userID: "user"},
	}}
	e.interactiveStates[key] = state
	s.steer = func(string) (bool, error) {
		// Model completion and a new arrival happen while the RPC is in flight.
		state.mu.Lock()
		if len(state.pendingMessages) != 2 || state.pendingMessages[1].queueID != "last" {
			t.Errorf("selected input was not claimed before RPC: %#v", state.pendingMessages)
		}
		state.pendingMessages = state.pendingMessages[1:]
		state.pendingMessages = append(state.pendingMessages, queuedMessage{queueID: "new", content: "new"})
		state.mu.Unlock()
		return false, nil
	}
	e.handleQueuedMessageChoice(p, &Message{SessionKey: key, UserID: "user"}, "chosen", false)
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.pendingMessages) != 3 || state.pendingMessages[0].content != "chosen" || state.pendingMessages[1].content != "last" || state.pendingMessages[2].content != "new" {
		t.Fatalf("restore moved chosen input behind later arrivals: %#v", state.pendingMessages)
	}
}

func TestQueuedSteering_ConcurrentClicksDoNotDuplicateOrStealInput(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s := &queuedSteeringSession{asyncInputSession: asyncInputSession{events: make(chan Event, 8), active: true}}
	e := NewEngine("test", &controllableAgent{nextSession: s}, []Platform{p}, "", LangEnglish)
	const key = "test:user"
	session := e.sessions.GetOrCreateActive(key)
	gen, _ := session.TryLock()
	defer session.Unlock(gen)
	state := &interactiveState{agentSession: s, busySession: session, pendingMessages: []queuedMessage{
		{queueID: "one", content: "first", userID: "user"},
		{queueID: "two", content: "second", userID: "user"},
	}}
	e.interactiveStates[key] = state
	s.steer = func(string) (bool, error) { close(entered); <-release; return true, nil }
	msg := &Message{SessionKey: key, UserID: "user"}
	go func() { defer close(done); e.handleQueuedMessageChoice(p, msg, "one", false) }()
	<-entered
	e.handleQueuedMessageChoice(p, msg, "one", false)
	e.handleQueuedMessageChoice(p, msg, "two", false)
	close(release)
	<-done
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.pendingMessages) != 1 || state.pendingMessages[0].queueID != "two" {
		t.Fatalf("concurrent click removed another message: %#v", state.pendingMessages)
	}
}

func TestQueuedSteering_ReplacementStateDoesNotReceiveRestoredInput(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	s := &queuedSteeringSession{asyncInputSession: asyncInputSession{events: make(chan Event, 8), active: true}}
	e := NewEngine("test", &controllableAgent{nextSession: s}, []Platform{p}, "", LangEnglish)
	const key = "test:user"
	old := &interactiveState{agentSession: s, pendingMessages: []queuedMessage{{queueID: "one", userID: "user"}}}
	replacement := &interactiveState{agentSession: s}
	e.interactiveStates[key] = old
	s.steer = func(string) (bool, error) {
		e.interactiveMu.Lock()
		e.interactiveStates[key] = replacement
		e.interactiveMu.Unlock()
		return false, nil
	}
	e.handleQueuedMessageChoice(p, &Message{SessionKey: key, UserID: "user"}, "one", false)
	if len(replacement.pendingMessages) != 0 {
		t.Fatal("expired input crossed into a replacement conversation")
	}
}

func TestQueuedSteering_UnsupportedAndAttachmentMessagesRetainQueueReceipt(t *testing.T) {
	for _, scenario := range []string{"no-steering", "image", "file", "anonymous"} {
		t.Run(scenario, func(t *testing.T) {
			p := &asyncInputPlatform{}
			p.n = "test"
			s := &asyncInputSession{events: make(chan Event, 8), active: true}
			e := NewEngine("test", &controllableAgent{nextSession: s}, []Platform{p}, "", LangEnglish)
			var agentSession AgentSession = s
			if scenario == "no-steering" {
				agentSession = &stubAgentSession{}
			}
			const key = "test:user"
			state := &interactiveState{agentSession: agentSession}
			e.interactiveStates[key] = state
			msg := &Message{SessionKey: key, Platform: "test", UserID: "user", Content: "follow-up"}
			switch scenario {
			case "image":
				msg.Images = []ImageAttachment{{MimeType: "image/png"}}
			case "file":
				msg.Files = []FileAttachment{{FileName: "notes.txt"}}
			case "anonymous":
				msg.UserID = ""
			}
			if !e.queueMessageForBusySession(p, msg, key) {
				t.Fatal("message was not queued")
			}
			if len(p.sentCards) != 0 || len(p.repliedCards) != 0 {
				t.Fatal("unsupported message received a steering control")
			}
			if !strings.Contains(strings.Join(p.getSent(), "\n"), e.i18n.T(MsgMessageQueued)) {
				t.Fatal("ordinary queue receipt missing")
			}
			if len(state.pendingMessages) != 1 {
				t.Fatal("ordinary queue lost the message")
			}
		})
	}
}

func TestQueuedSteering_RemovalRetiresCards(t *testing.T) {
	for _, mode := range []string{"recall", "session_ended", "silent_stop", "stale_drain", "dead_agent"} {
		t.Run(mode, func(t *testing.T) {
			p := &trackedQueuedChoicePlatform{}
			p.n = "test"
			s := &asyncInputSession{events: make(chan Event, 8), active: true}
			e := NewEngine("test", &controllableAgent{nextSession: s}, []Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
			t.Cleanup(func() { _ = e.Stop() })
			const key = "test:removed"
			session := e.sessions.GetOrCreateActive(key)
			gen, _ := session.TryLock()
			state := &interactiveState{agentSession: s, busySession: session}
			e.interactiveStates[key] = state
			e.queueMessageForBusySession(p, &Message{SessionKey: key, UserID: "author", Content: "queued task", MessageID: "original", ReplyCtx: "user-message", UserMessageTimeMs: 10}, key)
			switch mode {
			case "recall":
				if _, ok := e.removeQueuedMessageByID("original"); !ok {
					t.Fatal("recall failed")
				}
			case "session_ended":
				e.notifyDroppedQueuedMessages(state, errors.New("agent ended"))
			case "silent_stop":
				e.stopInteractiveSessionSilently(key)
			case "stale_drain":
				state.lastCompletedUserMessageTimeMs = 20
				e.drainPendingMessages(state, session, e.sessions, key, gen)
			case "dead_agent":
				_ = s.Close()
				e.drainPendingMessages(state, session, e.sessions, key, gen)
			}
			updates := p.getChoiceUpdates()
			if len(updates) != 1 || updates[0].ctx != "sent-card-1" {
				t.Fatalf("removed queue card was not retired: %#v", updates)
			}
			assertClosedQueuedChoice(t, updates[0].card, e.i18n.T(MsgQueueRemoved), "queued task")
			s.mu.Lock()
			defer s.mu.Unlock()
			if len(s.sent) != 0 || len(s.steered) != 0 {
				t.Fatal("removed message was sent")
			}
		})
	}
}

func TestQueuedSteering_ExpiredCallbackRepairsCard(t *testing.T) {
	for _, mode := range []string{"processed", "other_user", "other_chat", "previous_process", "typed_command"} {
		t.Run(mode, func(t *testing.T) {
			p := &trackedQueuedChoicePlatform{}
			p.n = "test"
			e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
			t.Cleanup(func() { _ = e.Stop() })
			q := queuedMessage{queueID: "one", platform: p, userID: "author", msgSessionKey: "test:chat", content: "queued task"}
			q.choiceReceipt = e.sendQueuedMessageChoice(p, "user-message", q)
			p.mu.Lock()
			p.updateErr = errors.New("card API unavailable")
			p.mu.Unlock()
			e.markQueuedMessageProcessing(q)
			p.mu.Lock()
			p.updateErr = nil
			p.mu.Unlock()
			msg := &Message{SessionKey: "test:chat", UserID: "author", ReplyCtx: "clicked-card"}
			id := "one"
			switch mode {
			case "other_user":
				msg.UserID = "stranger"
			case "other_chat":
				msg.SessionKey = "test:other"
			case "previous_process":
				id = "old-process-id"
			case "typed_command":
				id = "old-process-id"
				msg.ReplyCtx = "user-message"
			}
			e.handleQueuedMessageChoice(p, msg, id, false)
			updates := p.getChoiceUpdates()
			if mode == "other_user" || mode == "other_chat" || mode == "typed_command" {
				if len(updates) != 0 {
					t.Fatal("unauthorized or typed callback modified a card")
				}
				return
			}
			if len(updates) != 1 {
				t.Fatalf("stale button did not repair card: %v", updates)
			}
			status, content := MsgQueueProcessing, "queued task"
			if mode == "previous_process" {
				status, content = MsgQueueChoiceExpired, ""
			}
			assertClosedQueuedChoice(t, updates[0].card, e.i18n.T(status), content)
		})
	}
}

func TestQueuedSteering_RetryUsesLatestStatus(t *testing.T) {
	p := &trackedQueuedChoicePlatform{}
	p.n = "test"
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	q := queuedMessage{queueID: "one", platform: p, userID: "author", msgSessionKey: "test:chat", content: "queued task"}
	q.choiceReceipt = e.sendQueuedMessageChoice(p, "user-message", q)
	p.mu.Lock()
	p.updateErr = errors.New("temporary failure")
	p.mu.Unlock()
	e.finishQueuedMessageChoice(p, &Message{}, q, MsgQueueKept, MsgMessageQueued, "blue")
	e.markQueuedMessageProcessing(q)
	p.mu.Lock()
	p.updateErr = nil
	p.mu.Unlock()
	waitAsyncInput(t, func() bool { return len(p.getChoiceUpdates()) > 0 })
	for _, update := range p.getChoiceUpdates() {
		assertClosedQueuedChoice(t, update.card, e.i18n.T(MsgQueueProcessing), "queued task")
	}
}

func TestQueuedSteering_LateKeepDuringFailedProcessingUpdate(t *testing.T) {
	p := &trackedQueuedChoicePlatform{}
	p.n = "test"
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	q := queuedMessage{queueID: "one", platform: p, userID: "author", msgSessionKey: "test:chat", content: "queued task"}
	q.choiceReceipt = e.sendQueuedMessageChoice(p, "user-message", q)
	p.mu.Lock()
	p.updateErr = errors.New("temporary failure")
	p.mu.Unlock()
	e.markQueuedMessageProcessing(q)
	e.finishQueuedMessageChoice(p, &Message{}, q, MsgQueueKept, MsgMessageQueued, "blue")
	if len(p.getSent()) != 0 {
		t.Fatal("late Keep incorrectly reported a processing message as queued")
	}
}
