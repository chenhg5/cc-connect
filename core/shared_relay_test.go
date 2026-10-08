package core

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSharedRelayQuestionsAreAnswerableFromSourceChat(t *testing.T) {
	env, a := newSharedCompatEnv(t)
	done := make(chan error, 1)
	go func() {
		response, err := env.e.HandleRelay(context.Background(), "other", "test:user", "RELAY QUESTION")
		if err == nil && response != "RELAY RESULT" {
			err = fmt.Errorf("wrong relay response %q", response)
		}
		done <- err
	}()
	call := awaitSharedCompatCall(t, a)
	call.session.emit(Event{Type: EventPermissionRequest, TurnID: call.turn, RequestID: "relay-question", Questions: []UserQuestion{{ID: "q", Question: "Choose for relay", Options: []UserQuestionOption{{Label: "A"}}}}})
	env.await("Choose for relay")
	env.send(env.button("A"))
	call.session.mu.Lock()
	responses := append([]PermissionResult(nil), call.session.responses...)
	call.session.mu.Unlock()
	if len(responses) != 1 {
		t.Fatalf("source reply did not reach relay: %v", responses)
	}
	call.finish("RELAY RESULT")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("relay stuck")
	}
	if strings.Contains(env.visible(), "RELAY RESULT") {
		t.Fatal("relay sent duplicate final result instead of returning it")
	}
}

func TestSharedRelayTimeoutReturnsWithoutEventsAndPreservesPendingReader(t *testing.T) {
	env, a := newSharedCompatEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := env.e.HandleRelay(ctx, "other", "test:user", "RELAY TIMEOUT"); done <- err }()
	call := awaitSharedCompatCall(t, a)
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("timeout: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("no-event relay failed to honor cancellation")
	}
	call.session.emit(Event{Type: EventPermissionRequest, TurnID: call.turn, RequestID: "after-timeout", ToolInput: "LATE APPROVAL", Decisions: []string{"cancel"}})
	env.await("LATE APPROVAL")
	env.send(env.button(env.e.i18n.T(MsgSharedCancelDecision)))
	call.finish("LATE RESULT")
}
