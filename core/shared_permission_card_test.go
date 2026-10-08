package core

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

type trackedPermissionUpdate struct {
	handle int
	card   *Card
}

type sharedTrackedPermissionPlatform struct {
	*stubCardPlatform
	updates       chan trackedPermissionUpdate
	started       chan struct{}
	release       chan struct{}
	updateErr     error
	updateStarted chan struct{}
	updateRelease chan struct{}
}

func (p *sharedTrackedPermissionPlatform) SendCardWithHandle(ctx context.Context, reply any, card *Card) (any, error) {
	if err := p.SendCard(ctx, reply, card); err != nil {
		return nil, err
	}
	p.mu.Lock()
	handle := len(p.sentCards) - 1
	p.mu.Unlock()
	if p.started != nil {
		close(p.started)
		<-p.release
	}
	return handle, nil
}

func (p *sharedTrackedPermissionPlatform) UpdateCard(ctx context.Context, handle any, card *Card) error {
	if p.updateErr != nil {
		return p.updateErr
	}
	if p.updateStarted != nil {
		close(p.updateStarted)
		select {
		case <-p.updateRelease:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	index := handle.(int)
	p.mu.Lock()
	p.sentCards[index] = card
	p.mu.Unlock()
	p.updates <- trackedPermissionUpdate{index, card}
	return nil
}

func TestSharedPermissionCard_SlowExternalUpdateDoesNotBlockDaemonOutput(t *testing.T) {
	for _, local := range []bool{false, true} {
		name := "external update"
		if local {
			name = "local update in progress"
		}
		t.Run(name, func(t *testing.T) {
			env, p := newSharedTrackedPermissionEnv(t)
			p.updateStarted, p.updateRelease = make(chan struct{}), make(chan struct{})
			t.Cleanup(func() { close(p.updateRelease) })
			as := env.a.connection("first")
			as.emit(Event{Type: EventPermissionRequest, RequestID: "approval", ToolName: "Bash", ToolInput: "SLOW PATCH", Decisions: []string{"allow"}})
			env.await("SLOW PATCH")
			if local {
				go env.send("allow")
			} else {
				as.emit(Event{Type: EventPermissionResolved, RequestID: "approval"})
			}
			select {
			case <-p.updateStarted:
			case <-time.After(3 * time.Second):
				t.Fatal("card update did not start")
			}
			if local {
				as.emit(Event{Type: EventPermissionResolved, RequestID: "approval"})
			}
			as.emit(Event{Type: EventText, Content: "OUTPUT WHILE PATCH BLOCKED", Metadata: map[string]any{"phase": "commentary"}})
			env.await("OUTPUT WHILE PATCH BLOCKED")
		})
	}
}

func TestSharedPermissionCard_TextDecisionDoesNotDuplicateUpdatedCard(t *testing.T) {
	env, p := newSharedTrackedPermissionEnv(t)
	as := env.a.connection("first")
	as.emit(Event{Type: EventPermissionRequest, RequestID: "approval", ToolName: "Bash", ToolInput: "TEXT DECISION CARD", Decisions: []string{"allow"}})
	env.await("TEXT DECISION CARD")
	env.send("allow")
	update := awaitTrackedPermissionUpdate(t, p)
	if update.card.Header.Color != "green" || len(sharedPermissionButtons(update.card)) != 0 {
		t.Fatalf("text approval did not update card: %+v", update.card)
	}
	if strings.Contains(strings.Join(p.getSent(), "\n"), env.e.i18n.T(MsgSharedResponseSent)) {
		t.Fatal("successful card update sent a redundant confirmation message")
	}
}

func TestSharedPermissionCard_UpdateFailurePreservesTextConfirmation(t *testing.T) {
	env, p := newSharedTrackedPermissionEnv(t)
	p.updateErr = fmt.Errorf("simulated card update failure")
	as := env.a.connection("first")
	as.emit(Event{Type: EventPermissionRequest, RequestID: "approval", ToolName: "Bash", ToolInput: "FAILED UPDATE CARD", Decisions: []string{"allow"}})
	env.await("FAILED UPDATE CARD")
	env.send("allow")
	env.await(env.e.i18n.T(MsgSharedResponseSent))
}

func (p *sharedTrackedPermissionPlatform) getSent() []string {
	messages := p.stubPlatformEngine.getSent()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, card := range p.sentCards {
		messages = append(messages, card.RenderText())
	}
	return messages
}

func newSharedTrackedPermissionEnv(t *testing.T) (*sharedTestEnv, *sharedTrackedPermissionPlatform) {
	t.Helper()
	env := newSharedTestEnv(t)
	p := &sharedTrackedPermissionPlatform{
		stubCardPlatform: env.p,
		updates:          make(chan trackedPermissionUpdate, 16),
	}
	env.e.ReceiveMessage(p, &Message{SessionKey: "test:user", Platform: "test", UserID: "user", Content: "/attach first", ReplyCtx: "reply"})
	env.await("Attached to session first")
	return env, p
}

func awaitTrackedPermissionUpdate(t *testing.T, p *sharedTrackedPermissionPlatform) trackedPermissionUpdate {
	t.Helper()
	select {
	case update := <-p.updates:
		return update
	case <-time.After(3 * time.Second):
		t.Fatal("permission card was not updated after daemon completion")
		return trackedPermissionUpdate{}
	}
}

func TestSharedPermissionCard_ExternalResolutionUpdatesOriginalUnclickedCard(t *testing.T) {
	env, p := newSharedTrackedPermissionEnv(t)
	as := env.a.connection("first")
	as.emit(Event{Type: EventPermissionRequest, RequestID: "first-approval", ToolName: "Bash", ToolInput: "FIRST CARD", Decisions: []string{"allow", "cancel"}})
	env.await("FIRST CARD")
	oldAction := env.button(env.e.i18n.T(MsgPermBtnAllow))
	as.emit(Event{Type: EventPermissionRequest, RequestID: "next-approval", ToolName: "Bash", ToolInput: "NEXT CARD", Decisions: []string{"allow", "cancel"}})
	as.emit(Event{Type: EventPermissionResolved, RequestID: "first-approval"})
	update := awaitTrackedPermissionUpdate(t, p)
	if update.handle != 0 || update.card.Header.Color != "grey" || update.card.Header.Title != env.e.i18n.T(MsgSharedRequestResolved) || len(sharedPermissionButtons(update.card)) != 0 {
		t.Fatalf("external completion = %+v", update)
	}
	if !strings.Contains(update.card.RenderText(), "FIRST CARD") || strings.Contains(update.card.Header.Title, env.e.i18n.T(MsgPermBtnAllow)) {
		t.Fatalf("external completion lost context or guessed a decision: %+v", update.card)
	}
	env.await("NEXT CARD")
	// A duplicate daemon event and a stale click cannot resolve the next card.
	as.emit(Event{Type: EventPermissionResolved, RequestID: "first-approval"})
	env.send(oldAction)
	env.await(env.e.i18n.T(MsgSharedStaleRequest))
	as.emit(Event{Type: EventPermissionResolved, RequestID: "next-approval"})
	update = awaitTrackedPermissionUpdate(t, p)
	if update.handle != 1 || !strings.Contains(update.card.RenderText(), "NEXT CARD") || len(sharedPermissionButtons(update.card)) != 0 {
		t.Fatalf("wrong card updated by completion: %+v", update)
	}
	as.mu.Lock()
	defer as.mu.Unlock()
	if len(as.responses) != 0 {
		t.Fatalf("external completion submitted a decision: %+v", as.responses)
	}
}

func TestSharedPermissionCard_LocalDecisionSurvivesResolvedNotification(t *testing.T) {
	env, p := newSharedTrackedPermissionEnv(t)
	as := env.a.connection("first")
	as.emit(Event{Type: EventPermissionRequest, RequestID: "approval", ToolName: "Bash", ToolInput: "LOCAL CARD", Decisions: []string{"allow", "deny"}})
	env.await("LOCAL CARD")
	env.send(env.button(env.e.i18n.T(MsgPermBtnDeny)))
	update := awaitTrackedPermissionUpdate(t, p)
	if update.card.Header.Color != "red" {
		t.Fatalf("local deny state lost: %+v", update.card)
	}
	as.emit(Event{Type: EventPermissionResolved, RequestID: "approval"})
	// A subsequent approval proves the reader processed the completion event.
	as.emit(Event{Type: EventPermissionRequest, RequestID: "new", ToolName: "Bash", ToolInput: "NEW APPROVAL", Decisions: []string{"allow"}})
	env.await("NEW APPROVAL")
	p.mu.Lock()
	first := p.sentCards[0]
	p.mu.Unlock()
	if first.Header.Color != "red" || !strings.Contains(first.Header.Title, env.e.i18n.T(MsgPermBtnDeny)) {
		t.Fatalf("daemon notification overwrote local choice: %+v", first)
	}
	select {
	case extra := <-p.updates:
		t.Fatalf("duplicate completion update: %+v", extra)
	default:
	}
}

func TestSharedPermissionCard_ResolutionBeforeSendReturnsUpdatesCard(t *testing.T) {
	env, p := newSharedTrackedPermissionEnv(t)
	p.started, p.release = make(chan struct{}), make(chan struct{})
	as := env.a.connection("first")
	as.emit(Event{Type: EventPermissionRequest, RequestID: "approval", ToolName: "Bash", ToolInput: "SLOW SEND", Decisions: []string{"allow"}})
	select {
	case <-p.started:
	case <-time.After(3 * time.Second):
		t.Fatal("card send did not start")
	}
	env.send("allow")
	env.await(env.e.i18n.T(MsgSharedResponseSent))
	close(p.release)
	update := awaitTrackedPermissionUpdate(t, p)
	if update.card.Header.Color != "green" || len(sharedPermissionButtons(update.card)) != 0 {
		t.Fatalf("late send reintroduced pending approval: %+v", update.card)
	}
}

func sharedPermissionButtons(card *Card) []CardButton {
	var buttons []CardButton
	for _, element := range card.Elements {
		if actions, ok := element.(CardActions); ok {
			buttons = append(buttons, actions.Buttons...)
		}
	}
	return buttons
}

func TestSharedPermissionCard_DecisionUpdatesInPlace(t *testing.T) {
	for _, tc := range []struct {
		decision, color string
		label           MsgKey
	}{
		{"allow", "green", MsgPermBtnAllow}, {"deny", "red", MsgPermBtnDeny}, {"cancel", "grey", MsgSharedCancelDecision},
		{"allow_session", "green", MsgSharedAllowSession}, {"allow_similar_0", "green", MsgSharedAllowSimilar},
		{"network_allow", "green", MsgSharedNetworkAllow}, {"network_deny", "red", MsgSharedNetworkDeny},
	} {
		t.Run(tc.decision, func(t *testing.T) {
			env, p := newSharedTrackedPermissionEnv(t)
			as := env.a.connection("first")
			as.emit(Event{Type: EventPermissionRequest, RequestID: "approval", ToolName: "Bash", ToolInput: "CARD APPROVAL", ToolInputRaw: map[string]any{"reason": "Need access", "cwd": "/project"}, Decisions: []string{tc.decision}})
			env.await("CARD APPROVAL")
			label := env.e.i18n.T(tc.label)
			action := env.button(label)
			if !strings.HasPrefix(action, "/decision ") {
				t.Fatalf("permission button = %q; want request-bound command", action)
			}
			env.send(action)
			card := awaitTrackedPermissionUpdate(t, p).card
			if card == nil || card.Header == nil || card.Header.Color != tc.color || !strings.Contains(card.Header.Title, label) {
				t.Fatalf("resolved card = %+v; want %s %s", card, tc.color, label)
			}
			if len(sharedPermissionButtons(card)) != 0 || !strings.Contains(card.RenderText(), "CARD APPROVAL") || !strings.Contains(card.RenderText(), "Need access") || !strings.Contains(card.RenderText(), "/project") {
				t.Fatalf("resolved card lost context or retained buttons: %+v", card)
			}
			if strings.Contains(strings.Join(p.getSent(), "\n"), env.e.i18n.T(MsgSharedResponseSent)) {
				t.Fatal("card callback sent a separate confirmation message")
			}
			as.mu.Lock()
			defer as.mu.Unlock()
			if len(as.responses) != 1 || as.responses[0].Behavior != tc.decision {
				t.Fatalf("daemon responses = %+v", as.responses)
			}
		})
	}
}

func TestSharedPermissionCard_StaleAndFailedDecisionsNeverShowSuccess(t *testing.T) {
	env, p := newSharedTrackedPermissionEnv(t)
	as := env.a.connection("first")
	as.emit(Event{Type: EventPermissionRequest, RequestID: "approval", ToolName: "Bash", ToolInput: "RETRY APPROVAL", Decisions: []string{"allow", "deny"}})
	env.await("RETRY APPROVAL")
	action := env.button(env.e.i18n.T(MsgPermBtnAllow))
	for _, content := range []string{"/decision old-token allow", "/decision"} {
		env.send(content)
		env.await(env.e.i18n.T(MsgSharedStaleRequest))
	}
	env.e.ReceiveMessage(p, &Message{SessionKey: "test:other", Platform: "test", UserID: "other", Content: action, ReplyCtx: "other"})
	env.send(strings.TrimSuffix(action, " allow") + " allow_session")
	env.await(env.e.i18n.Tf(MsgSharedDecisionHint, "allow / deny"))
	as.mu.Lock()
	if len(as.responses) != 0 {
		t.Fatalf("invalid decisions submitted %+v", as.responses)
	}
	delete(as.pending, "approval")
	as.mu.Unlock()
	env.send(action)
	env.await("request no longer pending")
	select {
	case update := <-p.updates:
		t.Fatalf("failed decision updated the card: %+v", update)
	default:
	}
	as.mu.Lock()
	as.pending["approval"] = true
	as.mu.Unlock()
	env.send(action)
	if card := awaitTrackedPermissionUpdate(t, p).card; card.Header.Color != "green" {
		t.Fatalf("retry card = %+v", card)
	}
	env.send(action)
	env.await(env.e.i18n.T(MsgSharedStaleRequest))
	as.mu.Lock()
	defer as.mu.Unlock()
	if len(as.responses) != 1 {
		t.Fatalf("duplicate decision submitted: %+v", as.responses)
	}
}
